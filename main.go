package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

//go:embed configs/apps.json
var embeddedFiles embed.FS

const (
	configOverridePath = "/etc/fnos-mfs/apps.json"
	stateDir           = "/etc/fnos-mfs"
	serviceDir         = "/etc/systemd/system"
	poolRootName       = "mfs_pools"
	poolReadmeName     = ".readme.txt"
)

type Config struct {
	Apps []AppConfig `json:"apps"`
}

type AppConfig struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	DefaultPoolName string   `json:"default_pool_name"`
	DefaultMountDir string   `json:"default_mount_dir"`
	PathTemplate    string   `json:"path_template"`
	UserCandidates  []string `json:"user_candidates"`
	ServiceName     string   `json:"service_name"`
}

type Volume struct {
	Name       string
	Path       string
	Device     string
	FSType     string
	UUID       string
	MountState string
}

type OwnerCandidate struct {
	HomeName string `json:"home_name"`
	Path     string `json:"path"`
	UID      string `json:"uid"`
	GID      string `json:"gid"`
	UserName string `json:"user_name"`
	Group    string `json:"group"`
	Count    int    `json:"-"`
}

type BranchState struct {
	VolumePath string `json:"volume_path"`
	VolumeUUID string `json:"volume_uuid"`
	BranchPath string `json:"branch_path"`
}

type AppState struct {
	AppID       string         `json:"app_id"`
	AppLabel    string         `json:"app_label"`
	AppUser     string         `json:"app_user"`
	Owner       OwnerCandidate `json:"owner"`
	PoolName    string         `json:"pool_name"`
	MountPoint  string         `json:"mount_point"`
	ServiceName string         `json:"service_name"`
	Branches    []BranchState  `json:"branches"`
}

type SetupPlan struct {
	App        AppConfig
	AppUser    string
	Owner      OwnerCandidate
	PoolName   string
	MountPoint string
	Volumes    []Volume
	Branches   []BranchState
}

type commandOutputFunc func(name string, args ...string) string
type userLookupFunc func(username string) (*user.User, error)

type AppSelection struct {
	App   AppConfig
	Other bool
}

type StatusItem struct {
	State  string
	Label  string
	Detail string
}

type CLIOptions struct {
	English bool
	Help    bool
}

type unknownArgumentError struct {
	Arg string
}

func (err unknownArgumentError) Error() string {
	return ui("未知参数: ", "unknown argument: ") + err.Arg
}

const (
	statusOK      = "ok"
	statusWarn    = "warn"
	statusFail    = "fail"
	colorReset    = "\033[0m"
	colorGreen    = "\033[32m"
	colorYellow   = "\033[33m"
	colorRed      = "\033[31m"
	customAppID   = "other"
	customAppName = "Other"
)

var englishOutput bool
var runtimeStateDir = stateDir

var errDependencyInstallDeclined = errors.New("缺少依赖，已取消安装")
var errSetupPreflightFailed = errors.New("set 预检失败，请先处理红色项目")

const poolReadmeText = `这是 fnos-mfs 创建和管理的底层池目录。这里的隐藏目录，例如 .media_pool、.music_pool、.xunlei_pool、.aria2_pool，是 mergerfs 使用的真实数据分支；App 平时应该使用外面的聚合入口目录，而不是直接把这个目录当媒体库入口。请不要删除、移动或重命名 mfs_pools 以及里面的隐藏池目录，除非你已经停止对应的 fnos-mfs systemd 服务、确认挂载已经卸载，并且明确知道每个目录里保存的真实数据是什么。

This directory is created and managed by fnos-mfs. Hidden directories here, such as .media_pool, .music_pool, .xunlei_pool, and .aria2_pool, are real mergerfs branch directories; apps should normally use the visible merged mount directory outside this folder instead of using this folder as a media library entry. Do not delete, move, or rename mfs_pools or the hidden pool directories inside it unless the related fnos-mfs systemd service has been stopped, the mount has been unmounted, and you fully understand that these directories may contain real data.
`

func ui(cn string, en string) string {
	if englishOutput {
		return en
	}
	return cn
}

func appDisplayLabel(app AppConfig) string {
	if !englishOutput {
		return app.Label
	}
	switch app.ID {
	case "fnvideo":
		return "fnOS Video"
	case "fnmusic":
		return "fnOS Music"
	case "fnxunlei":
		return "fnOS Xunlei"
	case "fnaria2":
		return "Aria2"
	default:
		return app.Label
	}
}

func localizedErrorText(err error) string {
	if err == nil {
		return ""
	}
	if !englishOutput {
		return err.Error()
	}
	var unknown unknownArgumentError
	if errors.As(err, &unknown) {
		return "unknown argument: " + unknown.Arg
	}
	if errors.Is(err, errDependencyInstallDeclined) {
		return "missing dependencies; installation canceled"
	}
	if errors.Is(err, errSetupPreflightFailed) {
		return "set preflight failed; fix red items first"
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "没有找到") && strings.Contains(msg, "状态文件"):
		return "saved state file was not found; run set first: " + msg
	case strings.Contains(msg, "需要 root 权限"):
		return "root permission is required; run with sudo ./fnos-mfs"
	case strings.Contains(msg, "没有发现 /vol1 /vol2"):
		return "no /vol1 /vol2 style volumes were found"
	case strings.Contains(msg, "set 至少选择两个卷"):
		return "set requires at least two volumes"
	case strings.Contains(msg, "App 用户为空"):
		return "app user is empty; ACL cannot be applied"
	case strings.Contains(msg, "没有找到 apt"):
		return "apt was not found; dependencies cannot be installed automatically"
	}

	replacer := strings.NewReplacer(
		"创建底层目录失败", "failed to create branch directory",
		"写入底层池说明失败", "failed to write pool readme",
		"创建聚合入口失败", "failed to create merged mount directory",
		"设置 owner 失败", "failed to set owner",
		"设置 ACL 失败", "failed to set ACL",
		"更新 /etc/fuse.conf 失败", "failed to update /etc/fuse.conf",
		"写入状态文件失败", "failed to write state file",
		"写入 systemd 服务失败", "failed to write systemd service",
		"刷新 systemd 失败", "failed to reload systemd",
		"启用 systemd 服务失败", "failed to enable systemd service",
		"启动 systemd 服务失败", "failed to start systemd service",
		"停止当前 MFS 服务失败", "failed to stop current MFS service",
		"迁移旧版底层目录失败", "failed to migrate legacy branch directories",
		"设置父目录通行权限失败", "failed to set parent directory traverse ACL",
		"设置目录权限失败", "failed to set directory ACL",
		"设置默认权限失败", "failed to set default ACL",
		"apt update 失败", "apt update failed",
		"apt install 失败", "apt install failed",
	)
	return replacer.Replace(msg)
}

func messageContains(msg string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(msg, value) {
			return true
		}
	}
	return false
}

func parseCLIArgs(args []string) (CLIOptions, error) {
	var opts CLIOptions
	for _, arg := range args {
		switch arg {
		case "-en", "--en":
			opts.English = true
		case "-h", "-help", "--help":
			opts.Help = true
		default:
			return opts, unknownArgumentError{Arg: arg}
		}
	}
	return opts, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, ui("用法:", "Usage:"))
	fmt.Fprintln(w, "  fnos-mfs [-en]")
	fmt.Fprintln(w, "  fnos-mfs -h")
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui("选项:", "Options:"))
	fmt.Fprintln(w, ui("  -h, -help, --help  显示帮助并退出", "  -h, -help, --help  Show help and exit"))
	fmt.Fprintln(w, ui("  -en                使用英文交互提示", "  -en                Use English interactive text"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui("示例:", "Examples:"))
	fmt.Fprintln(w, "  sudo ./fnos-mfs")
	fmt.Fprintln(w, "  sudo ./fnos-mfs -en")
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui("说明: fnos-mfs 是交互式工具，正常使用不需要其他参数。", "Note: fnos-mfs is interactive and normally does not need other arguments."))
}

func main() {
	opts, err := parseCLIArgs(os.Args[1:])
	englishOutput = opts.English
	if err != nil {
		printError(err)
		fmt.Fprintln(os.Stderr)
		printUsage(os.Stderr)
		os.Exit(2)
	}
	if opts.Help {
		printUsage(os.Stdout)
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		exitErr(err)
	}
	if len(cfg.Apps) == 0 {
		exitErr(errors.New(ui("没有可用 App 配置", "no app config is available")))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("FNOS MFS")
	fmt.Println(ui("交互式 mergerfs 配置工具", "Interactive mergerfs configuration tool"))
	fmt.Println()

	selection, err := chooseApp(reader, cfg.Apps)
	if err != nil {
		exitErr(err)
	}
	app := selection.App

	if !selection.Other {
		printAppDashboard(app)
	}

	if selection.Other {
		app, err = promptCustomApp(reader)
		if err != nil {
			exitErr(err)
		}
	}

	err = runActionMenu(reader, app)
	if err != nil {
		exitErr(err)
	}
}

func runActionMenu(reader *bufio.Reader, app AppConfig) error {
	for {
		action, err := chooseOne(reader, ui("选择操作", "Choose action"), []string{
			ui("set - 配置/修改 MFS 聚合目录", "set - create/update an MFS merge"),
			ui("discover - 发现当前 /vol 卷", "discover - list current /vol volumes"),
			ui("acl - 给当前 App 补权限", "acl - re-apply ACL for this app"),
			ui("status - 查看当前 App 状态", "status - show this app status"),
			ui("install - 安装 mergerfs/fuse3/acl", "install - install mergerfs/fuse3/acl"),
			ui("exit - 退出", "exit - quit"),
		})
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Println()
				return nil
			}
			return err
		}

		switch {
		case strings.HasPrefix(action, "set"):
			err = runSet(reader, app)
		case strings.HasPrefix(action, "discover"):
			err = runDiscover()
		case strings.HasPrefix(action, "acl"):
			err = runACL(reader, app)
		case strings.HasPrefix(action, "status"):
			err = runStatus(app)
		case strings.HasPrefix(action, "install"):
			err = runInstall(reader)
		case strings.HasPrefix(action, "exit"):
			return nil
		}
		if err != nil {
			printError(err)
		}
	}
}

func loadConfig() (Config, error) {
	data, err := fs.ReadFile(embeddedFiles, "configs/apps.json")
	if err != nil {
		return Config{}, err
	}
	if override, err := os.ReadFile(configOverridePath); err == nil {
		data = override
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	cfg.Apps = appendSavedCustomApps(cfg.Apps)
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func appendSavedCustomApps(apps []AppConfig) []AppConfig {
	entries, err := os.ReadDir(runtimeStateDir)
	if os.IsNotExist(err) {
		return apps
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, ui("读取已保存 App 配置失败: %v\n", "Failed to read saved app configs: %v\n"), err)
		return apps
	}

	seen := map[string]bool{}
	for _, app := range apps {
		seen[app.ID] = true
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || entry.Name() == "apps.json" {
			continue
		}
		path := filepath.Join(runtimeStateDir, entry.Name())
		state, err := readStateFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, ui("跳过无效状态文件: %s: %v\n", "Skip invalid state file: %s: %v\n"), path, err)
			continue
		}
		appID := sanitizeID(firstNonEmpty(state.AppID, strings.TrimSuffix(entry.Name(), ".json")), "")
		if appID == "" || seen[appID] {
			continue
		}
		app := mergeCustomAppStateDefaults(customAppDefaults(appID), state)
		app.ID = appID
		apps = append(apps, app)
		seen[appID] = true
	}
	return apps
}

func validateConfig(cfg Config) error {
	if len(cfg.Apps) == 0 {
		return errors.New(ui("apps 不能为空", "apps cannot be empty"))
	}
	seen := map[string]bool{}
	for _, app := range cfg.Apps {
		switch {
		case app.ID == "":
			return errors.New(ui("app id 不能为空", "app id cannot be empty"))
		case seen[app.ID]:
			return fmt.Errorf(ui("app id 重复: %s", "duplicate app id: %s"), app.ID)
		case app.Label == "":
			return fmt.Errorf(ui("%s label 不能为空", "%s label cannot be empty"), app.ID)
		case app.DefaultPoolName == "":
			return fmt.Errorf(ui("%s default_pool_name 不能为空", "%s default_pool_name cannot be empty"), app.ID)
		case app.DefaultMountDir == "":
			return fmt.Errorf(ui("%s default_mount_dir 不能为空", "%s default_mount_dir cannot be empty"), app.ID)
		case app.PathTemplate == "":
			return fmt.Errorf(ui("%s path_template 不能为空", "%s path_template cannot be empty"), app.ID)
		case app.ServiceName == "":
			return fmt.Errorf(ui("%s service_name 不能为空", "%s service_name cannot be empty"), app.ID)
		}
		seen[app.ID] = true
	}
	return nil
}

func chooseApp(reader *bufio.Reader, apps []AppConfig) (AppSelection, error) {
	labels := make([]string, 0, len(apps)+1)
	for _, app := range apps {
		labels = append(labels, fmt.Sprintf("%s - %s", app.ID, appDisplayLabel(app)))
	}
	labels = append(labels, ui("other - 自定义 App", "other - custom app"))
	chosen, err := chooseOne(reader, ui("选择 App", "Choose app"), labels)
	if err != nil {
		return AppSelection{}, err
	}
	for i, label := range labels {
		if label == chosen {
			if i == len(apps) {
				return AppSelection{Other: true}, nil
			}
			return AppSelection{App: apps[i]}, nil
		}
	}
	return AppSelection{}, errors.New(ui("未选择 App", "no app selected"))
}

func promptCustomApp(reader *bufio.Reader) (AppConfig, error) {
	fmt.Println()
	fmt.Println(ui("自定义 App", "Custom app"))
	id, err := promptDefault(reader, "App ID", customAppID)
	if err != nil {
		return AppConfig{}, err
	}
	appID := sanitizeID(id, customAppID)
	defaults := customAppDefaults(appID)
	if state, err := loadState(defaults); err == nil {
		fmt.Printf(ui("已读取已有配置: %s\n", "Loaded existing config: %s\n"), filepath.Join(runtimeStateDir, appID+".json"))
		defaults = mergeCustomAppStateDefaults(defaults, state)
	}

	label, err := promptDefault(reader, ui("显示名称", "Display name"), defaults.Label)
	if err != nil {
		return AppConfig{}, err
	}
	userName, err := promptDefault(reader, ui("App Linux 用户名", "App Linux username"), firstString(defaults.UserCandidates))
	if err != nil {
		return AppConfig{}, err
	}
	poolName, err := promptDefault(reader, ui("默认底层目录名", "Default branch directory name"), defaults.DefaultPoolName)
	if err != nil {
		return AppConfig{}, err
	}
	poolName = normalizePoolNameWithNotice(poolName)
	mountDir, err := promptDefault(reader, ui("默认聚合入口目录名", "Default merged mount directory name"), defaults.DefaultMountDir)
	if err != nil {
		return AppConfig{}, err
	}
	var candidates []string
	if userName != "" {
		candidates = []string{userName}
	}
	return AppConfig{
		ID:              appID,
		Label:           label,
		DefaultPoolName: poolName,
		DefaultMountDir: mountDir,
		PathTemplate:    "{primary}/{home}/{mount_dir}",
		UserCandidates:  candidates,
		ServiceName:     defaults.ServiceName,
	}, nil
}

func customAppDefaults(appID string) AppConfig {
	return AppConfig{
		ID:              appID,
		Label:           customAppName,
		DefaultPoolName: ".mfs_pool",
		DefaultMountDir: ui("聚合目录", "Merged"),
		PathTemplate:    "{primary}/{home}/{mount_dir}",
		ServiceName:     "fnos-mfs-" + appID,
	}
}

func mergeCustomAppStateDefaults(app AppConfig, state AppState) AppConfig {
	if state.AppLabel != "" {
		app.Label = state.AppLabel
	}
	if state.AppUser != "" {
		app.UserCandidates = []string{state.AppUser}
	}
	if poolName := statePoolName(state); poolName != "" {
		app.DefaultPoolName = poolName
	}
	if state.MountPoint != "" {
		if mountDir := filepath.Base(filepath.Clean(state.MountPoint)); mountDir != "." && mountDir != string(os.PathSeparator) {
			app.DefaultMountDir = mountDir
		}
	}
	if state.ServiceName != "" {
		app.ServiceName = state.ServiceName
	}
	return app
}

func statePoolName(state AppState) string {
	if state.PoolName != "" {
		return normalizePoolName(state.PoolName)
	}
	for _, branch := range state.Branches {
		if branch.BranchPath != "" {
			return filepath.Base(filepath.Clean(branch.BranchPath))
		}
	}
	return ""
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func runSet(reader *bufio.Reader, app AppConfig) error {
	volumes, err := discoverVolumes()
	if err != nil {
		return err
	}
	if len(volumes) == 0 {
		return errors.New(ui("没有发现 /vol1 /vol2 这类卷", "no /vol1 /vol2 style volumes were found"))
	}

	selectedVolumes, err := chooseVolumes(reader, volumes)
	if err != nil {
		return err
	}
	if len(selectedVolumes) < 2 {
		return errors.New(ui("set 至少选择两个卷", "set requires at least two volumes"))
	}

	owners := discoverOwners(selectedVolumes)
	owner, err := chooseOwner(reader, owners)
	if err != nil {
		return err
	}

	appUser, _ := defaultAppUser(app)
	if appUser == "" {
		appUser, err = promptDefault(reader, ui("没有自动找到 App 用户，请输入真实 Linux 用户名", "App user was not detected; enter the real Linux username"), "")
		if err != nil {
			return err
		}
	}

	poolName, err := promptDefault(reader, ui("底层目录名", "Branch directory name"), app.DefaultPoolName)
	if err != nil {
		return err
	}
	poolName = normalizePoolNameWithNotice(poolName)
	defaultMount := renderPathTemplate(app.PathTemplate, selectedVolumes[0].Path, owner.HomeName, app.DefaultMountDir)
	mountPoint, err := promptDefault(reader, ui("聚合入口路径", "Merged mount path"), defaultMount)
	if err != nil {
		return err
	}

	plan := buildSetupPlan(app, appUser, owner, poolName, mountPoint, selectedVolumes)
	plan, err = maybeCustomizePlan(reader, plan)
	if err != nil {
		return err
	}
	printPlan(plan)
	checks := setupPlanChecks(plan, commandOutput, user.Lookup)
	printStatusList(ui("预检结果:", "Preflight checks:"), checks)
	if hasFailedStatus(checks) {
		return errSetupPreflightFailed
	}
	ok, err := confirm(reader, ui("确认执行 set", "Run set now"))
	if err != nil || !ok {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if err := ensureDependencies(reader); err != nil {
		return err
	}
	if err := applySetup(plan); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(ui("set 完成", "set completed"))
	return runStatus(app)
}

func runDiscover() error {
	volumes, err := discoverVolumes()
	if err != nil {
		return err
	}
	if len(volumes) == 0 {
		fmt.Println(ui("没有发现 /vol1 /vol2 这类卷", "no /vol1 /vol2 style volumes were found"))
		return nil
	}
	printVolumes(volumes)
	return nil
}

func printAppDashboard(app AppConfig) {
	fmt.Println(ui("当前 App 状态:", "Current app status:"))
	for _, item := range collectAppStatus(app) {
		fmt.Println("  " + renderStatusItem(item))
	}
	volumes, err := discoverVolumes()
	if err != nil {
		fmt.Println("  " + renderStatusItem(StatusItem{State: statusFail, Label: ui("卷发现", "Volume discovery"), Detail: localizedErrorText(err)}))
		fmt.Println()
		return
	}
	if len(volumes) == 0 {
		fmt.Println("  " + renderStatusItem(StatusItem{State: statusWarn, Label: ui("可用卷", "Available volumes"), Detail: ui("没有发现 /vol1 /vol2 这类卷", "no /vol1 /vol2 style volumes were found")}))
		fmt.Println()
		return
	}
	fmt.Println("  " + renderStatusItem(StatusItem{State: statusOK, Label: ui("可用卷", "Available volumes"), Detail: fmt.Sprintf(ui("%d 个", "%d"), len(volumes))}))
	for _, vol := range volumes {
		fmt.Printf("    - %s device=%s uuid=%s\n", vol.Path, emptyDash(vol.Device), emptyDash(vol.UUID))
	}
	fmt.Println()
}

func collectAppStatus(app AppConfig) []StatusItem {
	items := []StatusItem{
		{State: statusOK, Label: ui("预设", "Preset"), Detail: fmt.Sprintf("%s (%s)", app.ID, appDisplayLabel(app))},
		{State: statusOK, Label: ui("默认底层目录", "Default branch directory"), Detail: app.DefaultPoolName},
		{State: statusOK, Label: ui("底层根目录", "Branch root directory"), Detail: poolRootName},
		{State: statusOK, Label: ui("默认聚合入口名", "Default merged mount name"), Detail: app.DefaultMountDir},
	}
	items = append(items, hostStatusItems(exec.LookPath, os.Geteuid())...)
	if appUser, ok := appUserStatus(app); ok {
		items = append(items, StatusItem{State: statusOK, Label: ui("App 用户", "App user"), Detail: appUser})
	} else if len(app.UserCandidates) > 0 {
		items = append(items, StatusItem{State: statusFail, Label: ui("App 用户", "App user"), Detail: ui("未找到，候选: ", "not found; candidates: ") + strings.Join(app.UserCandidates, ", ")})
	} else {
		items = append(items, StatusItem{State: statusWarn, Label: ui("App 用户", "App user"), Detail: ui("没有配置候选用户", "no candidate users configured")})
	}
	items = append(items, dependencyStatusItems(exec.LookPath)...)
	if state, err := loadState(app); err == nil {
		items = append(items, StatusItem{State: statusOK, Label: ui("已保存配置", "Saved config"), Detail: state.MountPoint})
		if active := commandOutput("systemctl", "is-active", state.ServiceName+".service"); active == "active" {
			items = append(items, StatusItem{State: statusOK, Label: "systemd", Detail: state.ServiceName + ".service active"})
		} else if active != "" {
			items = append(items, StatusItem{State: statusWarn, Label: "systemd", Detail: state.ServiceName + ".service " + active})
		} else {
			items = append(items, StatusItem{State: statusWarn, Label: "systemd", Detail: ui("当前环境无法读取或服务未创建", "cannot read service state here or service is not created")})
		}
	} else {
		items = append(items, StatusItem{State: statusWarn, Label: ui("已保存配置", "Saved config"), Detail: ui("未找到，执行 set 后生成", "not found; run set to create it")})
	}
	return items
}

func hostStatusItems(lookPath func(string) (string, error), euid int) []StatusItem {
	items := make([]StatusItem, 0, 3)
	if euid == 0 {
		items = append(items, StatusItem{State: statusOK, Label: ui("Root 权限", "Root permission"), Detail: ui("当前是 root", "running as root")})
	} else {
		items = append(items, StatusItem{State: statusWarn, Label: ui("Root 权限", "Root permission"), Detail: ui("set/install/acl 需要 sudo", "set/install/acl need sudo")})
	}
	if _, err := lookPath("systemctl"); err == nil {
		items = append(items, StatusItem{State: statusOK, Label: "systemd", Detail: ui("可用", "available")})
	} else {
		items = append(items, StatusItem{State: statusFail, Label: "systemd", Detail: ui("未找到 systemctl", "systemctl not found")})
	}
	if _, err := lookPath("apt"); err == nil {
		items = append(items, StatusItem{State: statusOK, Label: "apt", Detail: ui("可用", "available")})
	} else {
		items = append(items, StatusItem{State: statusWarn, Label: "apt", Detail: ui("未找到，install 不能自动安装依赖", "not found; install cannot auto-install dependencies")})
	}
	return items
}

func dependencyStatusItems(lookPath func(string) (string, error)) []StatusItem {
	checks := []struct {
		label    string
		commands []string
	}{
		{label: "mergerfs", commands: []string{"mergerfs"}},
		{label: "fuse3", commands: []string{"fusermount3"}},
		{label: "acl", commands: []string{"setfacl", "getfacl"}},
	}
	items := make([]StatusItem, 0, len(checks))
	for _, check := range checks {
		var missing []string
		for _, command := range check.commands {
			if _, err := lookPath(command); err != nil {
				missing = append(missing, command)
			}
		}
		if len(missing) == 0 {
			items = append(items, StatusItem{State: statusOK, Label: check.label, Detail: ui("已安装", "installed")})
		} else {
			items = append(items, StatusItem{State: statusFail, Label: check.label, Detail: ui("未安装/未找到: ", "missing/not found: ") + strings.Join(missing, ", ")})
		}
	}
	return items
}

func renderStatusItem(item StatusItem) string {
	prefix := "?"
	color := colorYellow
	switch item.State {
	case statusOK:
		prefix = "OK"
		color = colorGreen
	case statusFail:
		prefix = "NO"
		color = colorRed
	case statusWarn:
		prefix = "--"
		color = colorYellow
	}
	text := fmt.Sprintf("[%s] %s: %s", prefix, item.Label, item.Detail)
	return colorize(color, text)
}

func colorize(color string, text string) string {
	if os.Getenv("NO_COLOR") != "" {
		return text
	}
	return color + text + colorReset
}

func runACL(reader *bufio.Reader, app AppConfig) error {
	state, err := loadState(app)
	if err != nil {
		return err
	}
	appUser := state.AppUser
	if appUser == "" {
		appUser = resolveAppUser(app)
	}
	if appUser == "" {
		appUser, err = promptDefault(reader, ui("没有自动找到 App 用户，请输入真实 Linux 用户名", "App user was not detected; enter the real Linux username"), "")
		if err != nil {
			return err
		}
	}
	ok, err := confirm(reader, fmt.Sprintf(ui("确认给 %s 补 ACL", "Apply ACL for %s"), appUser))
	if err != nil || !ok {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if err := applyACL(state, appUser); err != nil {
		return err
	}
	fmt.Println(ui("ACL 完成", "ACL completed"))
	return nil
}

func runStatus(app AppConfig) error {
	state, err := loadState(app)
	if err != nil {
		printAppDashboard(app)
		return nil
	}
	printAppDashboard(app)
	fmt.Println()
	fmt.Printf("App: %s (%s)\n", state.AppID, state.AppLabel)
	fmt.Printf("%s: %s\n", ui("App 用户", "App user"), emptyDash(state.AppUser))
	fmt.Printf("Owner: %s uid=%s gid=%s home=%s\n", emptyDash(state.Owner.UserName), state.Owner.UID, state.Owner.GID, state.Owner.HomeName)
	fmt.Printf("%s: %s\n", ui("挂载入口", "Merged mount"), state.MountPoint)
	fmt.Printf("%s: %s\n", ui("底层根目录", "Branch root"), poolRootName)
	fmt.Printf("%s: %s\n", ui("底层目录名", "Branch directory"), state.PoolName)
	fmt.Printf("systemd: %s\n", state.ServiceName)
	fmt.Println()
	for _, branch := range state.Branches {
		fmt.Printf("- %s uuid=%s branch=%s\n", branch.VolumePath, emptyDash(branch.VolumeUUID), branch.BranchPath)
	}
	fmt.Println()
	printStatusList(ui("保存配置检查:", "Saved config checks:"), stateStatusChecks(state))
	printCommand("systemctl", "is-active", state.ServiceName+".service")
	printCommand("findmnt", state.MountPoint)
	printCommand("df", "-hT", state.MountPoint)
	return nil
}

func runInstall(reader *bufio.Reader) error {
	ok, err := confirm(reader, ui("确认安装 mergerfs fuse3 acl", "Install mergerfs fuse3 acl"))
	if err != nil {
		return err
	}
	if !ok {
		return errDependencyInstallDeclined
	}
	if err := requireRoot(); err != nil {
		return err
	}
	return installDependencies()
}

func discoverVolumes() ([]Volume, error) {
	return discoverVolumesIn("/", commandOutput)
}

func discoverVolumesIn(root string, command commandOutputFunc) ([]Volume, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	volName := regexp.MustCompile(`^vol[0-9]+$`)
	var volumes []Volume
	for _, entry := range entries {
		if !entry.IsDir() || !volName.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if root == "/" {
			path = filepath.Join("/", entry.Name())
		}
		vol := Volume{Name: entry.Name(), Path: path}
		vol.Device = command("findmnt", "-no", "SOURCE", path)
		vol.FSType = command("findmnt", "-no", "FSTYPE", path)
		vol.UUID = findUUIDWithCommand(path, vol.Device, command)
		if vol.Device != "" {
			vol.MountState = "mounted"
		} else {
			vol.MountState = "unmounted"
		}
		volumes = append(volumes, vol)
	}
	sort.Slice(volumes, func(i, j int) bool {
		return naturalVolLess(volumes[i].Name, volumes[j].Name)
	})
	return volumes, nil
}

func findUUID(mountPoint string, device string) string {
	return findUUIDWithCommand(mountPoint, device, commandOutput)
}

func findUUIDWithCommand(mountPoint string, device string, command commandOutputFunc) string {
	uuid := command("findmnt", "-no", "UUID", mountPoint)
	if uuid != "" {
		return uuid
	}
	if device != "" {
		uuid = command("lsblk", "-no", "UUID", device)
	}
	return uuid
}

func discoverOwners(volumes []Volume) []OwnerCandidate {
	type key struct {
		homeName string
		uid      string
		gid      string
		userName string
		group    string
	}
	found := map[key]OwnerCandidate{}
	for _, vol := range volumes {
		entries, err := os.ReadDir(vol.Path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !isNumeric(entry.Name()) {
				continue
			}
			path := filepath.Join(vol.Path, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				continue
			}
			uid := strconv.FormatUint(uint64(stat.Uid), 10)
			gid := strconv.FormatUint(uint64(stat.Gid), 10)
			userName := lookupUserName(uid)
			groupName := lookupGroupName(gid)
			k := key{homeName: entry.Name(), uid: uid, gid: gid, userName: userName, group: groupName}
			candidate := found[k]
			candidate.HomeName = entry.Name()
			candidate.Path = path
			candidate.UID = uid
			candidate.GID = gid
			candidate.UserName = userName
			candidate.Group = groupName
			candidate.Count++
			found[k] = candidate
		}
	}
	var owners []OwnerCandidate
	for _, owner := range found {
		owners = append(owners, owner)
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].Count != owners[j].Count {
			return owners[i].Count > owners[j].Count
		}
		return owners[i].HomeName < owners[j].HomeName
	})
	return owners
}

func chooseOwner(reader *bufio.Reader, owners []OwnerCandidate) (OwnerCandidate, error) {
	if len(owners) == 0 {
		home, err := promptDefault(reader, ui("没有自动找到 owner home，请输入 home 目录名", "Owner home was not detected; enter the home directory name"), "1000")
		if err != nil {
			return OwnerCandidate{}, err
		}
		ownerName, err := promptDefault(reader, ui("请输入 owner 用户名", "Enter owner username"), "")
		if err != nil {
			return OwnerCandidate{}, err
		}
		u, _ := user.Lookup(ownerName)
		candidate := OwnerCandidate{HomeName: home, UserName: ownerName}
		if u != nil {
			candidate.UID = u.Uid
			candidate.GID = u.Gid
		}
		return candidate, nil
	}
	if len(owners) == 1 {
		owner := owners[0]
		fmt.Printf(ui("自动获取 owner: %s uid=%s gid=%s home=%s\n", "Detected owner: %s uid=%s gid=%s home=%s\n"), emptyDash(owner.UserName), owner.UID, owner.GID, owner.HomeName)
		return owner, nil
	}
	labels := make([]string, 0, len(owners))
	for _, owner := range owners {
		labels = append(labels, fmt.Sprintf(ui("home=%s owner=%s uid=%s gid=%s 命中=%d", "home=%s owner=%s uid=%s gid=%s matches=%d"), owner.HomeName, emptyDash(owner.UserName), owner.UID, owner.GID, owner.Count))
	}
	chosen, err := chooseOne(reader, ui("选择 owner", "Choose owner"), labels)
	if err != nil {
		return OwnerCandidate{}, err
	}
	for i, label := range labels {
		if label == chosen {
			return owners[i], nil
		}
	}
	return OwnerCandidate{}, errors.New(ui("未选择 owner", "no owner selected"))
}

func chooseVolumes(reader *bufio.Reader, volumes []Volume) ([]Volume, error) {
	labels := make([]string, 0, len(volumes))
	for _, vol := range volumes {
		labels = append(labels, volumeLabel(vol))
	}
	selectedLabels, err := chooseMany(reader, ui("选择参与 MFS 的卷", "Choose volumes for MFS"), labels)
	if err != nil {
		return nil, err
	}
	selected := make([]Volume, 0, len(selectedLabels))
	for _, label := range selectedLabels {
		for i, candidate := range labels {
			if label == candidate {
				selected = append(selected, volumes[i])
			}
		}
	}
	return selected, nil
}

func resolveAppUser(app AppConfig) string {
	appUser, ok := appUserStatus(app)
	if ok {
		fmt.Printf(ui("自动获取 App 用户: %s\n", "Detected app user: %s\n"), appUser)
		return appUser
	}
	return ""
}

func appUserStatus(app AppConfig) (string, bool) {
	for _, candidate := range app.UserCandidates {
		if _, err := user.Lookup(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func defaultAppUser(app AppConfig) (string, bool) {
	if appUser, ok := appUserStatus(app); ok {
		fmt.Printf(ui("自动获取 App 用户: %s\n", "Detected app user: %s\n"), appUser)
		return appUser, true
	}
	if len(app.UserCandidates) > 0 {
		return app.UserCandidates[0], false
	}
	return "", false
}

func maybeCustomizePlan(reader *bufio.Reader, plan SetupPlan) (SetupPlan, error) {
	ok, err := confirm(reader, ui("修改 appuser/name/path 等其他选项", "Edit app user, branch name, mount path, or other options"))
	if err != nil || !ok {
		return plan, err
	}
	appUser, err := promptDefault(reader, ui("App 用户", "App user"), plan.AppUser)
	if err != nil {
		return plan, err
	}
	poolName, err := promptDefault(reader, ui("底层目录名", "Branch directory name"), plan.PoolName)
	if err != nil {
		return plan, err
	}
	poolName = normalizePoolNameWithNotice(poolName)
	mountPoint, err := promptDefault(reader, ui("聚合入口路径", "Merged mount path"), plan.MountPoint)
	if err != nil {
		return plan, err
	}
	return buildSetupPlan(plan.App, appUser, plan.Owner, poolName, mountPoint, plan.Volumes), nil
}

func buildSetupPlan(app AppConfig, appUser string, owner OwnerCandidate, poolName string, mountPoint string, volumes []Volume) SetupPlan {
	poolName = normalizePoolName(poolName)
	branches := make([]BranchState, 0, len(volumes))
	for _, vol := range volumes {
		branches = append(branches, BranchState{
			VolumePath: vol.Path,
			VolumeUUID: vol.UUID,
			BranchPath: filepath.Join(vol.Path, owner.HomeName, poolRootName, poolName),
		})
	}
	return SetupPlan{
		App: app, AppUser: appUser, Owner: owner, PoolName: poolName,
		MountPoint: mountPoint, Volumes: volumes, Branches: branches,
	}
}

func setupPlanChecks(plan SetupPlan, command commandOutputFunc, lookupUser userLookupFunc) []StatusItem {
	var checks []StatusItem
	add := func(state string, label string, detail string) {
		checks = append(checks, StatusItem{State: state, Label: label, Detail: detail})
	}

	if len(plan.Volumes) >= 2 {
		add(statusOK, ui("卷数量", "Volume count"), fmt.Sprintf(ui("%d 个", "%d"), len(plan.Volumes)))
	} else {
		add(statusFail, ui("卷数量", "Volume count"), ui("至少选择两个卷", "select at least two volumes"))
	}

	seenVolumes := map[string]bool{}
	for _, vol := range plan.Volumes {
		if seenVolumes[vol.Path] {
			add(statusFail, ui("重复卷", "Duplicate volume"), vol.Path)
			continue
		}
		seenVolumes[vol.Path] = true
		if vol.Device == "" || vol.MountState != "mounted" {
			add(statusFail, ui("卷未挂载", "Volume is not mounted"), fmt.Sprintf("%s device=%s", vol.Path, emptyDash(vol.Device)))
			continue
		}
		detail := fmt.Sprintf("%s device=%s fstype=%s uuid=%s", vol.Path, emptyDash(vol.Device), emptyDash(vol.FSType), emptyDash(vol.UUID))
		if vol.UUID == "" {
			add(statusWarn, ui("卷 UUID", "Volume UUID"), detail)
		} else {
			add(statusOK, ui("卷", "Volume"), detail)
		}
	}

	if isValidPoolName(plan.PoolName) {
		add(statusOK, ui("底层目录名", "Branch directory name"), plan.PoolName)
	} else {
		add(statusFail, ui("底层目录名无效", "Invalid branch directory name"), plan.PoolName)
	}

	if filepath.IsAbs(plan.MountPoint) {
		add(statusOK, ui("聚合入口路径", "Merged mount path"), plan.MountPoint)
	} else {
		add(statusFail, ui("聚合入口路径必须是绝对路径", "Merged mount path must be absolute"), plan.MountPoint)
	}

	if plan.AppUser == "" {
		add(statusFail, ui("App 用户", "App user"), ui("为空", "empty"))
	} else if _, err := lookupUser(plan.AppUser); err == nil {
		add(statusOK, ui("App 用户", "App user"), plan.AppUser)
	} else {
		add(statusFail, ui("App 用户不存在", "App user does not exist"), plan.AppUser)
	}

	if plan.Owner.HomeName == "" {
		add(statusFail, "Owner home", ui("为空", "empty"))
	} else if plan.Owner.UID == "" || plan.Owner.GID == "" {
		add(statusFail, ui("Owner UID/GID 缺失", "Owner UID/GID missing"), fmt.Sprintf("home=%s uid=%s gid=%s", plan.Owner.HomeName, emptyDash(plan.Owner.UID), emptyDash(plan.Owner.GID)))
	} else {
		add(statusOK, "Owner", fmt.Sprintf("home=%s uid=%s gid=%s", plan.Owner.HomeName, plan.Owner.UID, plan.Owner.GID))
	}

	seenBranches := map[string]bool{}
	for _, branch := range plan.Branches {
		if seenBranches[branch.BranchPath] {
			add(statusFail, ui("重复分支目录", "Duplicate branch directory"), branch.BranchPath)
			continue
		}
		seenBranches[branch.BranchPath] = true
		if samePath(branch.BranchPath, plan.MountPoint) || pathInside(plan.MountPoint, branch.BranchPath) {
			add(statusFail, ui("挂载入口冲突", "Mount path conflict"), fmt.Sprintf(ui("%s 在底层目录 %s 内", "%s is inside branch directory %s"), plan.MountPoint, branch.BranchPath))
		} else if pathInside(branch.BranchPath, plan.MountPoint) {
			add(statusFail, ui("挂载入口冲突", "Mount path conflict"), fmt.Sprintf(ui("底层目录 %s 在挂载入口 %s 内", "branch directory %s is inside mount path %s"), branch.BranchPath, plan.MountPoint))
		}

		legacyPath := legacyBranchPath(plan, branch)
		if legacyPath != branch.BranchPath {
			if migrationStatus, ok := legacyBranchMigrationStatus(legacyPath, branch.BranchPath); ok {
				add(migrationStatus.State, migrationStatus.Label, migrationStatus.Detail)
			}
		}
	}

	if source := command("findmnt", "-no", "SOURCE", plan.MountPoint); source != "" {
		if isCurrentAppMountSource(source, plan.App.ServiceName) {
			add(statusWarn, ui("挂载入口已由当前服务挂载", "Mount path is already mounted by this service"), fmt.Sprintf("%s source=%s", plan.MountPoint, source))
		} else {
			add(statusFail, ui("挂载入口已挂载", "Mount path is already mounted"), fmt.Sprintf("%s source=%s", plan.MountPoint, source))
		}
	}

	return checks
}

func stateStatusChecks(state AppState) []StatusItem {
	var checks []StatusItem
	add := func(status string, label string, detail string) {
		checks = append(checks, StatusItem{State: status, Label: label, Detail: detail})
	}
	if state.MountPoint == "" {
		add(statusFail, ui("聚合入口", "Merged mount"), ui("为空", "empty"))
	} else if _, err := os.Stat(state.MountPoint); err == nil {
		add(statusOK, ui("聚合入口", "Merged mount"), state.MountPoint)
	} else {
		add(statusFail, ui("聚合入口不存在", "Merged mount does not exist"), state.MountPoint)
	}
	for _, branch := range state.Branches {
		if _, err := os.Stat(branch.BranchPath); err == nil {
			add(statusOK, ui("底层目录", "Branch directory"), branch.BranchPath)
		} else {
			add(statusFail, ui("底层目录不存在", "Branch directory does not exist"), branch.BranchPath)
		}
		if isLegacyStateBranch(state, branch) {
			add(statusWarn, ui("旧版底层目录", "Legacy branch directory"), fmt.Sprintf(ui("%s 重新 set 后会迁移到 %s", "%s will be moved to %s after running set again"), branch.BranchPath, stateNewBranchPath(state, branch)))
		}
		if branch.VolumePath == "" {
			add(statusFail, ui("卷路径", "Volume path"), ui("为空", "empty"))
		} else if _, err := os.Stat(branch.VolumePath); err == nil {
			add(statusOK, ui("卷路径", "Volume path"), branch.VolumePath)
		} else {
			add(statusFail, ui("卷路径不存在", "Volume path does not exist"), branch.VolumePath)
		}
	}
	return checks
}

func isLegacyStateBranch(state AppState, branch BranchState) bool {
	return stateNewBranchPath(state, branch) != "" && filepath.Clean(branch.BranchPath) != stateNewBranchPath(state, branch)
}

func stateNewBranchPath(state AppState, branch BranchState) string {
	poolName := statePoolName(state)
	if branch.VolumePath == "" || state.Owner.HomeName == "" || poolName == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(branch.VolumePath, state.Owner.HomeName, poolRootName, poolName))
}

func isValidPoolName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, string(os.PathSeparator)+":")
}

func normalizePoolNameWithNotice(name string) string {
	normalized := normalizePoolName(name)
	if normalized != strings.TrimSpace(name) {
		fmt.Printf(ui("已识别 %s 前缀，底层目录名使用: %s\n", "Detected %s prefix; branch directory name will be: %s\n"), poolRootName, normalized)
	}
	return normalized
}

func normalizePoolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	cleaned := filepath.Clean(name)
	prefix := poolRootName + string(os.PathSeparator)
	if strings.HasPrefix(cleaned, prefix) {
		rest := strings.TrimPrefix(cleaned, prefix)
		if rest != "" && !strings.Contains(rest, string(os.PathSeparator)) {
			return rest
		}
	}
	return name
}

func isCurrentAppMountSource(source string, serviceName string) bool {
	source = strings.TrimSpace(source)
	serviceName = strings.TrimSpace(serviceName)
	return source != "" && serviceName != "" && source == serviceName
}

func legacyBranchPath(plan SetupPlan, branch BranchState) string {
	if plan.Owner.HomeName == "" || plan.PoolName == "" || branch.VolumePath == "" {
		return ""
	}
	return filepath.Join(branch.VolumePath, plan.Owner.HomeName, plan.PoolName)
}

func legacyBranchMigrationStatus(legacyPath string, branchPath string) (StatusItem, bool) {
	info, err := os.Stat(legacyPath)
	if os.IsNotExist(err) {
		return StatusItem{}, false
	}
	if err != nil {
		return StatusItem{State: statusFail, Label: ui("旧版底层目录检查失败", "Legacy branch check failed"), Detail: fmt.Sprintf("%s: %v", legacyPath, err)}, true
	}
	if !info.IsDir() {
		return StatusItem{State: statusFail, Label: ui("旧版底层目录不是目录", "Legacy branch is not a directory"), Detail: legacyPath}, true
	}

	targetInfo, err := os.Stat(branchPath)
	if os.IsNotExist(err) {
		return StatusItem{State: statusWarn, Label: ui("旧版底层目录", "Legacy branch directory"), Detail: fmt.Sprintf(ui("%s 将迁移到 %s", "%s will be moved to %s"), legacyPath, branchPath)}, true
	}
	if err != nil {
		return StatusItem{State: statusFail, Label: ui("新底层目录检查失败", "New branch check failed"), Detail: fmt.Sprintf("%s: %v", branchPath, err)}, true
	}
	if !targetInfo.IsDir() {
		return StatusItem{State: statusFail, Label: ui("新底层路径不是目录", "New branch path is not a directory"), Detail: branchPath}, true
	}
	empty, err := isDirEmpty(branchPath)
	if err != nil {
		return StatusItem{State: statusFail, Label: ui("新底层目录检查失败", "New branch check failed"), Detail: fmt.Sprintf("%s: %v", branchPath, err)}, true
	}
	if empty {
		return StatusItem{State: statusWarn, Label: ui("旧版底层目录", "Legacy branch directory"), Detail: fmt.Sprintf(ui("%s 将迁移到空目录 %s", "%s will be moved into empty directory %s"), legacyPath, branchPath)}, true
	}
	return StatusItem{State: statusFail, Label: ui("旧版底层目录冲突", "Legacy branch conflict"), Detail: fmt.Sprintf(ui("旧目录 %s 和新目录 %s 都存在且新目录非空，请先手动确认", "legacy %s and new %s both exist, and the new directory is not empty; check manually first"), legacyPath, branchPath)}, true
}

func isDirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func hasFailedStatus(items []StatusItem) bool {
	for _, item := range items {
		if item.State == statusFail {
			return true
		}
	}
	return false
}

func printStatusList(title string, items []StatusItem) {
	fmt.Println(title)
	for _, item := range items {
		fmt.Println("  " + renderStatusItem(item))
	}
	fmt.Println()
}

func samePath(a string, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func pathInside(path string, parent string) bool {
	path = filepath.Clean(path)
	parent = filepath.Clean(parent)
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func applySetup(plan SetupPlan) error {
	if err := stopCurrentAppMount(plan); err != nil {
		return fmt.Errorf(ui("停止当前 MFS 服务失败: %w", "failed to stop current MFS service: %w"), err)
	}
	if err := migrateLegacyBranches(plan); err != nil {
		return fmt.Errorf(ui("迁移旧版底层目录失败: %w", "failed to migrate legacy branch directories: %w"), err)
	}
	for _, branch := range plan.Branches {
		if err := os.MkdirAll(branch.BranchPath, 0775); err != nil {
			return fmt.Errorf(ui("创建底层目录失败 %s: %w", "failed to create branch directory %s: %w"), branch.BranchPath, err)
		}
	}
	if err := writePoolReadmes(plan.Branches); err != nil {
		return fmt.Errorf(ui("写入底层池说明失败: %w", "failed to write pool readme: %w"), err)
	}
	if err := os.MkdirAll(plan.MountPoint, 0775); err != nil {
		return fmt.Errorf(ui("创建聚合入口失败 %s: %w", "failed to create merged mount directory %s: %w"), plan.MountPoint, err)
	}
	chownTargets := append(branchPaths(plan.Branches), poolRootPaths(plan.Branches)...)
	chownTargets = append(chownTargets, poolReadmePaths(plan.Branches)...)
	chownTargets = append(chownTargets, plan.MountPoint)
	if err := chownIfKnown(plan.Owner, chownTargets); err != nil {
		return fmt.Errorf(ui("设置 owner 失败: %w", "failed to set owner: %w"), err)
	}
	state := AppState{
		AppID: plan.App.ID, AppLabel: plan.App.Label, AppUser: plan.AppUser,
		Owner: plan.Owner, PoolName: plan.PoolName, MountPoint: plan.MountPoint,
		ServiceName: plan.App.ServiceName, Branches: plan.Branches,
	}
	if err := applyACL(state, plan.AppUser); err != nil {
		return fmt.Errorf(ui("设置 ACL 失败: %w", "failed to set ACL: %w"), err)
	}
	if err := ensureFuseAllowOther(); err != nil {
		return fmt.Errorf(ui("更新 /etc/fuse.conf 失败: %w", "failed to update /etc/fuse.conf: %w"), err)
	}
	if err := writeState(state); err != nil {
		return fmt.Errorf(ui("写入状态文件失败: %w", "failed to write state file: %w"), err)
	}
	if err := writeService(state); err != nil {
		return fmt.Errorf(ui("写入 systemd 服务失败: %w", "failed to write systemd service: %w"), err)
	}
	if err := runCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf(ui("刷新 systemd 失败: %w", "failed to reload systemd: %w"), err)
	}
	if err := runCommand("systemctl", "enable", state.ServiceName+".service"); err != nil {
		return fmt.Errorf(ui("启用 systemd 服务失败: %w", "failed to enable systemd service: %w"), err)
	}
	if err := runCommand("systemctl", "restart", state.ServiceName+".service"); err != nil {
		return fmt.Errorf(ui("启动 systemd 服务失败: %w", "failed to start systemd service: %w"), err)
	}
	return nil
}

func stopCurrentAppMount(plan SetupPlan) error {
	source := commandOutput("findmnt", "-no", "SOURCE", plan.MountPoint)
	if !isCurrentAppMountSource(source, plan.App.ServiceName) {
		return nil
	}
	return runCommand("systemctl", "stop", plan.App.ServiceName+".service")
}

func migrateLegacyBranches(plan SetupPlan) error {
	for _, branch := range plan.Branches {
		legacyPath := legacyBranchPath(plan, branch)
		if legacyPath == "" || legacyPath == branch.BranchPath {
			continue
		}
		info, err := os.Stat(legacyPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", legacyPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf(ui("旧版底层路径不是目录: %s", "legacy branch path is not a directory: %s"), legacyPath)
		}

		targetInfo, err := os.Stat(branch.BranchPath)
		switch {
		case err == nil:
			if !targetInfo.IsDir() {
				return fmt.Errorf(ui("新底层路径不是目录: %s", "new branch path is not a directory: %s"), branch.BranchPath)
			}
			empty, err := isDirEmpty(branch.BranchPath)
			if err != nil {
				return fmt.Errorf("%s: %w", branch.BranchPath, err)
			}
			if !empty {
				return fmt.Errorf(ui("旧目录 %s 和新目录 %s 都存在且新目录非空", "legacy %s and new %s both exist, and the new directory is not empty"), legacyPath, branch.BranchPath)
			}
			if err := os.Remove(branch.BranchPath); err != nil {
				return fmt.Errorf("%s: %w", branch.BranchPath, err)
			}
		case os.IsNotExist(err):
			// Continue below.
		default:
			return fmt.Errorf("%s: %w", branch.BranchPath, err)
		}

		if err := os.MkdirAll(filepath.Dir(branch.BranchPath), 0775); err != nil {
			return fmt.Errorf("%s: %w", filepath.Dir(branch.BranchPath), err)
		}
		fmt.Printf(ui("迁移旧版底层目录: %s -> %s\n", "Move legacy branch directory: %s -> %s\n"), legacyPath, branch.BranchPath)
		if err := os.Rename(legacyPath, branch.BranchPath); err != nil {
			return fmt.Errorf("%s -> %s: %w", legacyPath, branch.BranchPath, err)
		}
	}
	return nil
}

func applyACL(state AppState, appUser string) error {
	if appUser == "" {
		return errors.New(ui("App 用户为空，不能补 ACL", "app user is empty; ACL cannot be applied"))
	}
	for _, ancestor := range aclAncestors(state) {
		if err := runCommand("setfacl", "-m", "u:"+appUser+":--x", ancestor); err != nil {
			return fmt.Errorf(ui("设置父目录通行权限失败 %s: %w", "failed to set parent directory traverse ACL %s: %w"), ancestor, err)
		}
	}
	targets := append(branchPaths(state.Branches), state.MountPoint)
	for _, target := range targets {
		if err := runCommand("setfacl", "-Rm", "u:"+appUser+":rwx", target); err != nil {
			return fmt.Errorf(ui("设置目录权限失败 %s: %w", "failed to set directory ACL %s: %w"), target, err)
		}
		if err := runCommand("setfacl", "-dm", "u:"+appUser+":rwx", target); err != nil {
			return fmt.Errorf(ui("设置默认权限失败 %s: %w", "failed to set default ACL %s: %w"), target, err)
		}
	}
	return nil
}

func aclAncestors(state AppState) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		if path == "" || path == "/" || seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}
	for _, branch := range state.Branches {
		add(branch.VolumePath)
		add(filepath.Join(branch.VolumePath, state.Owner.HomeName))
		add(filepath.Dir(branch.BranchPath))
	}
	add(filepath.Dir(state.MountPoint))
	return paths
}

func writePoolReadmes(branches []BranchState) error {
	for _, path := range poolReadmePaths(branches) {
		if err := os.WriteFile(path, []byte(poolReadmeText), 0644); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func writeState(state AppState) error {
	if err := os.MkdirAll(runtimeStateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(runtimeStateDir, state.AppID+".json"), data, 0644)
}

func loadState(app AppConfig) (AppState, error) {
	path := filepath.Join(runtimeStateDir, app.ID+".json")
	state, err := readStateFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AppState{}, fmt.Errorf(ui("没有找到 %s 状态文件，请先 set: %w", "saved state file for %s was not found; run set first: %w"), app.ID, err)
		}
		return AppState{}, err
	}
	return state, nil
}

func readStateFile(path string) (AppState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppState{}, err
	}
	var state AppState
	if err := json.Unmarshal(data, &state); err != nil {
		return AppState{}, err
	}
	return state, nil
}

func writeService(state AppState) error {
	mergerfsPath := firstPath("mergerfs", "/usr/bin/mergerfs")
	fusermountPath := firstPath("fusermount3", "/usr/bin/fusermount3")
	service := renderService(state, mergerfsPath, fusermountPath)
	return os.WriteFile(filepath.Join(serviceDir, state.ServiceName+".service"), []byte(service), 0644)
}

func renderService(state AppState, mergerfsPath string, fusermountPath string) string {
	branches := strings.Join(branchPaths(state.Branches), ":")
	options := "defaults,allow_other,use_ino,cache.files=off,category.create=mfs,moveonenospc=true,minfreespace=10G,fsname=" + state.ServiceName + ",umask=000"
	requires := append(branchPaths(state.Branches), state.MountPoint)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "[Unit]\n")
	fmt.Fprintf(&buf, "Description=FNOS MFS %s\n", state.AppID)
	fmt.Fprintf(&buf, "After=local-fs.target\n")
	fmt.Fprintf(&buf, "RequiresMountsFor=%s\n\n", systemdPathList(requires))
	fmt.Fprintf(&buf, "[Service]\n")
	fmt.Fprintf(&buf, "Type=simple\n")
	fmt.Fprintf(&buf, "Environment=%s\n", systemdEnv("MFS_MERGERFS", mergerfsPath))
	fmt.Fprintf(&buf, "Environment=%s\n", systemdEnv("MFS_FUSERMOUNT", fusermountPath))
	fmt.Fprintf(&buf, "Environment=%s\n", systemdEnv("MFS_OPTIONS", options))
	fmt.Fprintf(&buf, "Environment=%s\n", systemdEnv("MFS_BRANCHES", branches))
	fmt.Fprintf(&buf, "Environment=%s\n", systemdEnv("MFS_MOUNT", state.MountPoint))
	fmt.Fprintf(&buf, "ExecStart=/bin/sh -c 'exec \"$MFS_MERGERFS\" -f -o \"$MFS_OPTIONS\" \"$MFS_BRANCHES\" \"$MFS_MOUNT\"'\n")
	fmt.Fprintf(&buf, "ExecStop=/bin/sh -c 'exec \"$MFS_FUSERMOUNT\" -u \"$MFS_MOUNT\"'\n")
	fmt.Fprintf(&buf, "Restart=on-failure\n")
	fmt.Fprintf(&buf, "RestartSec=3\n\n")
	fmt.Fprintf(&buf, "[Install]\n")
	fmt.Fprintf(&buf, "WantedBy=multi-user.target\n")
	return buf.String()
}

func systemdEnv(key string, value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", "").Replace(key + "=" + value)
	return `"` + escaped + `"`
}

func systemdPathList(paths []string) string {
	escaped := make([]string, 0, len(paths))
	for _, path := range paths {
		escaped = append(escaped, systemdPathValue(path))
	}
	return strings.Join(escaped, " ")
}

func systemdPathValue(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch r {
		case ' ':
			b.WriteString(`\x20`)
		case '\t':
			b.WriteString(`\x09`)
		case '\n', '\r':
			// Unit values are line based. Drop line breaks rather than emitting
			// an invalid directive.
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ensureFuseAllowOther() error {
	path := "/etc/fuse.conf"
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "user_allow_other" {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString("user_allow_other\n")
	return err
}

func ensureDependencies(reader *bufio.Reader) error {
	missing := missingCommands([]string{"mergerfs", "setfacl", "getfacl", "fusermount3"})
	if len(missing) == 0 {
		return nil
	}
	fmt.Printf(ui("缺少命令: %s\n", "Missing commands: %s\n"), strings.Join(missing, ", "))
	ok, err := confirm(reader, ui("现在安装依赖", "Install dependencies now"))
	if err != nil {
		return err
	}
	if !ok {
		return errDependencyInstallDeclined
	}
	return installDependencies()
}

func installDependencies() error {
	if _, err := exec.LookPath("apt"); err != nil {
		return errors.New(ui("没有找到 apt，无法自动安装", "apt was not found; dependencies cannot be installed automatically"))
	}
	if err := runCommand("apt", "update"); err != nil {
		return fmt.Errorf(ui("apt update 失败: %w", "apt update failed: %w"), err)
	}
	if err := runCommand("apt", "install", "-y", "mergerfs", "fuse3", "acl"); err != nil {
		return fmt.Errorf(ui("apt install 失败: %w", "apt install failed: %w"), err)
	}
	return nil
}

func missingCommands(commands []string) []string {
	var missing []string
	for _, command := range commands {
		if _, err := exec.LookPath(command); err != nil {
			missing = append(missing, command)
		}
	}
	return missing
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New(ui("需要 root 权限，请用 sudo ./fnos-mfs 运行", "root permission is required; run with sudo ./fnos-mfs"))
	}
	return nil
}

func runCommand(name string, args ...string) error {
	fmt.Printf(ui("执行: %s %s\n", "Run: %s %s\n"), name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func printCommand(name string, args ...string) {
	fmt.Printf("$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf(ui("命令失败: %v\n", "Command failed: %v\n"), err)
	}
	fmt.Println()
}

func commandOutput(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstPath(command string, fallback string) string {
	if path, err := exec.LookPath(command); err == nil {
		return path
	}
	return fallback
}

func chownIfKnown(owner OwnerCandidate, paths []string) error {
	if owner.UID == "" || owner.GID == "" {
		return nil
	}
	uid, err := strconv.Atoi(owner.UID)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(owner.GID)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func branchPaths(branches []BranchState) []string {
	paths := make([]string, 0, len(branches))
	for _, branch := range branches {
		paths = append(paths, branch.BranchPath)
	}
	return paths
}

func poolRootPaths(branches []BranchState) []string {
	seen := map[string]bool{}
	var paths []string
	for _, branch := range branches {
		path := filepath.Dir(branch.BranchPath)
		if path == "." || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func poolReadmePaths(branches []BranchState) []string {
	roots := poolRootPaths(branches)
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, poolReadmeName))
	}
	return paths
}

func printVolumes(volumes []Volume) {
	fmt.Println()
	fmt.Println(ui("发现卷：", "Discovered volumes:"))
	for _, vol := range volumes {
		fmt.Printf("- %s status=%s device=%s fstype=%s uuid=%s\n", vol.Path, emptyDash(vol.MountState), emptyDash(vol.Device), emptyDash(vol.FSType), emptyDash(vol.UUID))
	}
	fmt.Println()
}

func printPlan(plan SetupPlan) {
	fmt.Println()
	fmt.Println(ui("执行计划：", "Execution plan:"))
	fmt.Printf("App: %s (%s)\n", plan.App.ID, appDisplayLabel(plan.App))
	fmt.Printf("%s: %s\n", ui("App 用户", "App user"), emptyDash(plan.AppUser))
	fmt.Printf("Owner: %s uid=%s gid=%s home=%s\n", emptyDash(plan.Owner.UserName), plan.Owner.UID, plan.Owner.GID, plan.Owner.HomeName)
	fmt.Printf("%s: %s\n", ui("底层根目录", "Branch root"), poolRootName)
	fmt.Printf("%s: %s\n", ui("底层目录名", "Branch directory"), plan.PoolName)
	fmt.Printf("%s: %s\n", ui("聚合入口", "Merged mount"), plan.MountPoint)
	fmt.Printf("systemd: %s.service\n", plan.App.ServiceName)
	fmt.Println(ui("分支目录：", "Branch directories:"))
	for _, branch := range plan.Branches {
		fmt.Printf("- %s uuid=%s -> %s\n", branch.VolumePath, emptyDash(branch.VolumeUUID), branch.BranchPath)
	}
	fmt.Println()
}

func chooseOne(reader *bufio.Reader, title string, options []string) (string, error) {
	if len(options) == 0 {
		return "", errors.New(ui("没有选项", "no options available"))
	}
	fmt.Println(title + ":")
	for i, option := range options {
		fmt.Printf("  %d. %s\n", i+1, option)
	}
	for {
		input, err := prompt(reader, ui("输入编号", "Enter number"))
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(input)
		if err == nil && n >= 1 && n <= len(options) {
			fmt.Println()
			return options[n-1], nil
		}
		fmt.Println(ui("编号无效", "Invalid number"))
	}
}

func chooseMany(reader *bufio.Reader, title string, options []string) ([]string, error) {
	if len(options) == 0 {
		return nil, errors.New(ui("没有选项", "no options available"))
	}
	fmt.Println(title + ":")
	for i, option := range options {
		fmt.Printf("  %d. [ ] %s\n", i+1, option)
	}
	fmt.Println(ui("输入示例：1,2,3 或 all", "Example input: 1,2,3 or all"))
	for {
		input, err := prompt(reader, ui("选择", "Select"))
		if err != nil {
			return nil, err
		}
		indexes, err := parseSelection(input, len(options))
		if err != nil {
			fmt.Println(err)
			continue
		}
		selected := make([]string, 0, len(indexes))
		for _, idx := range indexes {
			selected = append(selected, options[idx])
		}
		fmt.Println()
		return selected, nil
	}
}

func parseSelection(input string, count int) ([]int, error) {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "all" || input == "*" {
		indexes := make([]int, 0, count)
		for i := 0; i < count; i++ {
			indexes = append(indexes, i)
		}
		return indexes, nil
	}
	seen := map[int]bool{}
	var indexes []int
	parts := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > count {
			return nil, fmt.Errorf(ui("无效编号: %s", "invalid number: %s"), part)
		}
		idx := n - 1
		if !seen[idx] {
			seen[idx] = true
			indexes = append(indexes, idx)
		}
	}
	if len(indexes) == 0 {
		return nil, errors.New(ui("至少选择一个", "select at least one"))
	}
	sort.Ints(indexes)
	return indexes, nil
}

func promptDefault(reader *bufio.Reader, label string, def string) (string, error) {
	suffix := ""
	if def != "" {
		suffix = " [" + def + "]"
	}
	input, err := prompt(reader, label+suffix)
	if err != nil {
		return "", err
	}
	if input == "" {
		return def, nil
	}
	return input, nil
}

func confirm(reader *bufio.Reader, label string) (bool, error) {
	for {
		input, err := prompt(reader, label+ui("? yes/y=确认，no/n/回车=取消", "? yes/y=confirm, no/n/Enter=cancel"))
		if err != nil {
			return false, err
		}
		ok, valid := parseConfirm(input)
		if valid {
			return ok, nil
		}
		fmt.Println(ui("请输入 yes/y 或 no/n；直接回车取消。", "Enter yes/y or no/n; press Enter to cancel."))
	}
}

func parseConfirm(input string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "yes", "y":
		return true, true
	case "", "no", "n":
		return false, true
	default:
		return false, false
	}
}

func prompt(reader *bufio.Reader, label string) (string, error) {
	fmt.Print(label + ": ")
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

func renderPathTemplate(template string, primary string, home string, mountDir string) string {
	replacer := strings.NewReplacer(
		"{primary}", primary,
		"{home}", home,
		"{mount_dir}", mountDir,
	)
	return replacer.Replace(template)
}

func volumeLabel(vol Volume) string {
	return fmt.Sprintf("%s status=%s device=%s fstype=%s uuid=%s", vol.Path, emptyDash(vol.MountState), emptyDash(vol.Device), emptyDash(vol.FSType), emptyDash(vol.UUID))
}

func naturalVolLess(a string, b string) bool {
	ai := volNumber(a)
	bi := volNumber(b)
	if ai == bi {
		return a < b
	}
	return ai < bi
}

func volNumber(name string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(name, "vol"))
	if err != nil {
		return 0
	}
	return n
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func lookupUserName(uid string) string {
	u, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return u.Username
}

func lookupGroupName(gid string) string {
	g, err := user.LookupGroupId(gid)
	if err != nil {
		return ""
	}
	return g.Name
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func sanitizeID(value string, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fallback
	}
	return out
}

func exitErr(err error) {
	printError(err)
	os.Exit(1)
}

func printError(err error) {
	fmt.Fprintln(os.Stderr, ui("错误:", "Error:"), localizedErrorText(err))
	suggestions := repairSuggestions(err)
	if len(suggestions) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, ui("修复建议:", "Repair suggestions:"))
	for _, suggestion := range suggestions {
		fmt.Fprintln(os.Stderr, " - "+suggestion)
	}
}

func repairSuggestions(err error) []string {
	if err == nil {
		return nil
	}
	msg := err.Error()
	displayMsg := localizedErrorText(err)
	matches := func(values ...string) bool {
		return messageContains(msg, values...) || messageContains(displayMsg, values...)
	}
	var suggestions []string
	add := func(s string) {
		for _, existing := range suggestions {
			if existing == s {
				return
			}
		}
		suggestions = append(suggestions, s)
	}
	switch {
	case matches("需要 root 权限", "root permission is required"):
		add(ui("用 sudo 运行：sudo ./fnos-mfs", "Run with sudo: sudo ./fnos-mfs"))
	case matches("没有发现 /vol1 /vol2", "no /vol1 /vol2"):
		add(ui("先在 fnOS 里确认存储空间已挂载，再运行 discover。", "Confirm storage spaces are mounted in fnOS, then run discover."))
		add(ui("可用命令检查：ls -ld /vol* && findmnt | grep /vol", "You can check with: ls -ld /vol* && findmnt | grep /vol"))
	case matches("set 至少选择两个卷", "set requires at least two volumes"):
		add(ui("set 做聚合至少选两个卷，例如选择 1,2 或 2,3,4。", "set needs at least two volumes, for example 1,2 or 2,3,4."))
	case errors.Is(err, errSetupPreflightFailed):
		add(ui("按预检结果先处理红色项目，再重新执行 set。", "Fix the red preflight items first, then run set again."))
		add(ui("常见处理：安装依赖、确认 /volX 已挂载、确认 App 用户存在。", "Common fixes: install dependencies, confirm /volX is mounted, and confirm the app user exists."))
	case (matches("没有找到", "not found") && matches("状态文件", "state file")) || matches("saved state file"):
		add(ui("先执行 set 生成 /etc/fnos-mfs/<app>.json。", "Run set first to create /etc/fnos-mfs/<app>.json."))
	case errors.Is(err, errDependencyInstallDeclined):
		add(ui("重新进入 install，或手动执行：apt update && apt install -y mergerfs fuse3 acl", "Enter install again, or run manually: apt update && apt install -y mergerfs fuse3 acl"))
	case matches("没有找到 apt", "apt was not found"):
		add(ui("当前系统没有 apt，需手动安装 mergerfs、fuse3、acl。", "apt is not available on this system; install mergerfs, fuse3, and acl manually."))
	case matches("App 用户为空", "app user is empty"):
		add(ui("回到 set，选择修改其他选项，填写真实 App Linux 用户名。", "Go back to set, choose to edit other options, and enter the real app Linux username."))
	case matches("创建底层目录失败", "failed to create branch directory") || matches("创建聚合入口失败", "failed to create merged mount directory"):
		add(ui("确认所选 /volX 已挂载，且 home 目录存在，例如 /vol1/1000。", "Confirm the selected /volX paths are mounted and the home directory exists, for example /vol1/1000."))
		add(ui("确认用 sudo/root 运行。", "Confirm you are running as sudo/root."))
	case matches("卷未挂载", "Volume is not mounted"):
		add(ui("先在 fnOS 存储空间页面确认每块盘都是单独 Basic 存储空间。", "In fnOS storage settings, confirm each disk is an independent Basic storage space."))
		add(ui("再用 discover 确认 /volX 有 device/fstype/uuid。", "Then run discover and confirm /volX has device/fstype/uuid values."))
	case matches("底层目录名无效", "Invalid branch directory name"):
		add(ui("底层目录名只能是单个目录名，例如 .media_pool，不能包含 / 或 :。", "The branch directory name must be a single name such as .media_pool; it cannot contain / or :."))
	case matches("聚合入口路径必须是绝对路径", "Merged mount path must be absolute"):
		add(ui("聚合入口应类似 /vol1/1000/影视聚合。", "The merged mount path should look like /vol1/1000/影视聚合."))
	case matches("挂载入口已挂载", "Mount path is already mounted"):
		add(ui("先确认该入口是否已被旧服务使用：findmnt <path>。", "Check whether the path is already used by an old service: findmnt <path>."))
		add(ui("如果是旧 MFS，请先停止并卸载对应 systemd 服务，再重新执行 set。", "If it is an old MFS mount, stop and unmount the related systemd service before running set again."))
	case matches("旧版底层目录冲突", "Legacy branch conflict") || matches("旧目录", "legacy") && matches("新目录非空", "new directory is not empty"):
		add(ui("旧版 .media_pool 和新版 mfs_pools/.media_pool 同时存在时，工具不会自动合并，避免误覆盖真实数据。", "When legacy .media_pool and new mfs_pools/.media_pool both exist, the tool will not merge them automatically to avoid overwriting real data."))
		add(ui("先手动确认两个目录内容，再保留一个真实数据目录或手动合并后重新执行 set。", "Manually inspect both directories first, keep one real data directory or merge them manually, then run set again."))
	case matches("迁移旧版底层目录失败", "failed to migrate legacy branch directories"):
		add(ui("确认当前 App 的旧 systemd 服务已经停止，再重新执行 set。", "Confirm the old systemd service for this app has stopped, then run set again."))
		add(ui("不要删除旧版 .media_pool；里面可能是真实媒体数据。", "Do not delete the legacy .media_pool; it may contain real media data."))
	case matches("Owner UID/GID 缺失", "Owner UID/GID missing"):
		add(ui("选择能在 /volX/<home> 上自动识别到 uid/gid 的 owner。", "Choose an owner whose uid/gid can be detected from /volX/<home>."))
		add(ui("如果没有自动识别，先确认 /volX/1000 这类 home 目录存在并有正确属主。", "If nothing is detected, confirm /volX/1000-style home directories exist and have the correct owner."))
	case matches("setfacl"):
		add(ui("确认 acl 已安装：apt install -y acl", "Confirm acl is installed: apt install -y acl"))
		add(ui("确认 App 用户存在：id <appuser>", "Confirm the app user exists: id <appuser>"))
		add(ui("确认目标目录存在且在选中的 /volX 下。", "Confirm the target directory exists under the selected /volX paths."))
	case matches("apt update", "apt install"):
		add(ui("检查网络和 apt 源，然后重试 install。", "Check network and apt sources, then retry install."))
		add(ui("也可以手动执行：apt update && apt install -y mergerfs fuse3 acl", "You can also run manually: apt update && apt install -y mergerfs fuse3 acl"))
	case matches("systemctl"):
		add(ui("查看服务状态：systemctl status <service> --no-pager", "Check service status: systemctl status <service> --no-pager"))
		add(ui("查看详细日志：journalctl -u <service> -n 100 --no-pager", "Check logs: journalctl -u <service> -n 100 --no-pager"))
	case matches("/etc/fuse.conf"):
		add(ui("确认 fuse3 已安装，并用 sudo/root 运行。", "Confirm fuse3 is installed and run as sudo/root."))
	case matches("exit status"):
		add(ui("查看上一条失败命令的输出；若是 systemd，继续执行：systemctl status <service> --no-pager", "Read the previous failed command output; for systemd, run: systemctl status <service> --no-pager"))
		add(ui("若是 ACL 失败，确认 App 用户存在：id <appuser>", "For ACL failures, confirm the app user exists: id <appuser>"))
	case matches("permission denied", "operation not permitted"):
		add(ui("确认使用 sudo/root 运行，并确认目标目录属于 fnOS 存储空间。", "Run as sudo/root and confirm the target directory is inside fnOS storage."))
	}
	return suggestions
}
