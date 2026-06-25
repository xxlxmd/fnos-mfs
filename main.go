package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
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

type AppSelection struct {
	App   AppConfig
	Other bool
}

type StatusItem struct {
	State  string
	Label  string
	Detail string
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

func main() {
	if len(os.Args) > 1 {
		fmt.Println("fnos-mfs 是交互式工具，不需要参数。直接运行：fnos-mfs")
		os.Exit(2)
	}

	cfg, err := loadConfig()
	if err != nil {
		exitErr(err)
	}
	if len(cfg.Apps) == 0 {
		exitErr(errors.New("没有可用 App 配置"))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("FNOS MFS")
	fmt.Println("交互式 mergerfs 配置工具")
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
	action, err := chooseOne(reader, "选择操作", []string{
		"set - 配置/修改 MFS 聚合目录",
		"discover - 发现当前 /vol 卷",
		"acl - 给当前 App 补权限",
		"status - 查看当前 App 状态",
		"install - 安装 mergerfs/fuse3/acl",
		"exit - 退出",
	})
	if err != nil {
		exitErr(err)
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
	return err
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
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if len(cfg.Apps) == 0 {
		return errors.New("apps 不能为空")
	}
	seen := map[string]bool{}
	for _, app := range cfg.Apps {
		switch {
		case app.ID == "":
			return errors.New("app id 不能为空")
		case seen[app.ID]:
			return fmt.Errorf("app id 重复: %s", app.ID)
		case app.Label == "":
			return fmt.Errorf("%s label 不能为空", app.ID)
		case app.DefaultPoolName == "":
			return fmt.Errorf("%s default_pool_name 不能为空", app.ID)
		case app.DefaultMountDir == "":
			return fmt.Errorf("%s default_mount_dir 不能为空", app.ID)
		case app.PathTemplate == "":
			return fmt.Errorf("%s path_template 不能为空", app.ID)
		case app.ServiceName == "":
			return fmt.Errorf("%s service_name 不能为空", app.ID)
		}
		seen[app.ID] = true
	}
	return nil
}

func chooseApp(reader *bufio.Reader, apps []AppConfig) (AppSelection, error) {
	labels := make([]string, 0, len(apps)+1)
	for _, app := range apps {
		labels = append(labels, fmt.Sprintf("%s - %s", app.ID, app.Label))
	}
	labels = append(labels, "other - 自定义 App")
	chosen, err := chooseOne(reader, "选择 App", labels)
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
	return AppSelection{}, errors.New("未选择 App")
}

func promptCustomApp(reader *bufio.Reader) (AppConfig, error) {
	fmt.Println()
	fmt.Println("自定义 App")
	id, err := promptDefault(reader, "App ID", customAppID)
	if err != nil {
		return AppConfig{}, err
	}
	label, err := promptDefault(reader, "显示名称", customAppName)
	if err != nil {
		return AppConfig{}, err
	}
	userName, err := promptDefault(reader, "App Linux 用户名", "")
	if err != nil {
		return AppConfig{}, err
	}
	poolName, err := promptDefault(reader, "默认底层目录名", ".mfs_pool")
	if err != nil {
		return AppConfig{}, err
	}
	mountDir, err := promptDefault(reader, "默认聚合入口目录名", "聚合目录")
	if err != nil {
		return AppConfig{}, err
	}
	var candidates []string
	if userName != "" {
		candidates = []string{userName}
	}
	return AppConfig{
		ID:              sanitizeID(id, customAppID),
		Label:           label,
		DefaultPoolName: poolName,
		DefaultMountDir: mountDir,
		PathTemplate:    "{primary}/{home}/{mount_dir}",
		UserCandidates:  candidates,
		ServiceName:     "fnos-mfs-" + sanitizeID(id, customAppID),
	}, nil
}

func runSet(reader *bufio.Reader, app AppConfig) error {
	volumes, err := discoverVolumes()
	if err != nil {
		return err
	}
	if len(volumes) == 0 {
		return errors.New("没有发现 /vol1 /vol2 这类卷")
	}

	selectedVolumes, err := chooseVolumes(reader, volumes)
	if err != nil {
		return err
	}
	if len(selectedVolumes) < 2 {
		return errors.New("set 至少选择两个卷")
	}

	owners := discoverOwners(selectedVolumes)
	owner, err := chooseOwner(reader, owners)
	if err != nil {
		return err
	}

	appUser, _ := defaultAppUser(app)
	if appUser == "" {
		appUser, err = promptDefault(reader, "没有自动找到 App 用户，请输入真实 Linux 用户名", "")
		if err != nil {
			return err
		}
	}

	poolName, err := promptDefault(reader, "底层目录名", app.DefaultPoolName)
	if err != nil {
		return err
	}
	defaultMount := renderPathTemplate(app.PathTemplate, selectedVolumes[0].Path, owner.HomeName, app.DefaultMountDir)
	mountPoint, err := promptDefault(reader, "聚合入口路径", defaultMount)
	if err != nil {
		return err
	}

	plan := buildSetupPlan(app, appUser, owner, poolName, mountPoint, selectedVolumes)
	plan, err = maybeCustomizePlan(reader, plan)
	if err != nil {
		return err
	}
	printPlan(plan)
	ok, err := confirm(reader, "确认执行 set")
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
	fmt.Println("set 完成")
	return runStatus(app)
}

func runDiscover() error {
	volumes, err := discoverVolumes()
	if err != nil {
		return err
	}
	if len(volumes) == 0 {
		fmt.Println("没有发现 /vol1 /vol2 这类卷")
		return nil
	}
	printVolumes(volumes)
	return nil
}

func printAppDashboard(app AppConfig) {
	fmt.Println("当前 App 状态:")
	for _, item := range collectAppStatus(app) {
		fmt.Println("  " + renderStatusItem(item))
	}
	volumes, err := discoverVolumes()
	if err != nil {
		fmt.Println("  " + renderStatusItem(StatusItem{State: statusFail, Label: "卷发现", Detail: err.Error()}))
		fmt.Println()
		return
	}
	if len(volumes) == 0 {
		fmt.Println("  " + renderStatusItem(StatusItem{State: statusWarn, Label: "可用卷", Detail: "没有发现 /vol1 /vol2 这类卷"}))
		fmt.Println()
		return
	}
	fmt.Println("  " + renderStatusItem(StatusItem{State: statusOK, Label: "可用卷", Detail: fmt.Sprintf("%d 个", len(volumes))}))
	for _, vol := range volumes {
		fmt.Printf("    - %s device=%s uuid=%s\n", vol.Path, emptyDash(vol.Device), emptyDash(vol.UUID))
	}
	fmt.Println()
}

func collectAppStatus(app AppConfig) []StatusItem {
	items := []StatusItem{
		{State: statusOK, Label: "预设", Detail: fmt.Sprintf("%s (%s)", app.ID, app.Label)},
		{State: statusOK, Label: "默认底层目录", Detail: app.DefaultPoolName},
		{State: statusOK, Label: "默认聚合入口名", Detail: app.DefaultMountDir},
	}
	if appUser, ok := appUserStatus(app); ok {
		items = append(items, StatusItem{State: statusOK, Label: "App 用户", Detail: appUser})
	} else if len(app.UserCandidates) > 0 {
		items = append(items, StatusItem{State: statusFail, Label: "App 用户", Detail: "未找到，候选: " + strings.Join(app.UserCandidates, ", ")})
	} else {
		items = append(items, StatusItem{State: statusWarn, Label: "App 用户", Detail: "没有配置候选用户"})
	}
	items = append(items, dependencyStatusItems(exec.LookPath)...)
	if state, err := loadState(app); err == nil {
		items = append(items, StatusItem{State: statusOK, Label: "已保存配置", Detail: state.MountPoint})
		if active := commandOutput("systemctl", "is-active", state.ServiceName+".service"); active == "active" {
			items = append(items, StatusItem{State: statusOK, Label: "systemd", Detail: state.ServiceName + ".service active"})
		} else if active != "" {
			items = append(items, StatusItem{State: statusWarn, Label: "systemd", Detail: state.ServiceName + ".service " + active})
		} else {
			items = append(items, StatusItem{State: statusWarn, Label: "systemd", Detail: "当前环境无法读取或服务未创建"})
		}
	} else {
		items = append(items, StatusItem{State: statusWarn, Label: "已保存配置", Detail: "未找到，执行 set 后生成"})
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
			items = append(items, StatusItem{State: statusOK, Label: check.label, Detail: "已安装"})
		} else {
			items = append(items, StatusItem{State: statusFail, Label: check.label, Detail: "未安装/未找到: " + strings.Join(missing, ", ")})
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
		appUser, err = promptDefault(reader, "没有自动找到 App 用户，请输入真实 Linux 用户名", "")
		if err != nil {
			return err
		}
	}
	ok, err := confirm(reader, fmt.Sprintf("确认给 %s 补 ACL", appUser))
	if err != nil || !ok {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if err := applyACL(state, appUser); err != nil {
		return err
	}
	fmt.Println("ACL 完成")
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
	fmt.Printf("App 用户: %s\n", emptyDash(state.AppUser))
	fmt.Printf("Owner: %s uid=%s gid=%s home=%s\n", emptyDash(state.Owner.UserName), state.Owner.UID, state.Owner.GID, state.Owner.HomeName)
	fmt.Printf("挂载入口: %s\n", state.MountPoint)
	fmt.Printf("底层目录名: %s\n", state.PoolName)
	fmt.Printf("systemd: %s\n", state.ServiceName)
	fmt.Println()
	for _, branch := range state.Branches {
		fmt.Printf("- %s uuid=%s branch=%s\n", branch.VolumePath, emptyDash(branch.VolumeUUID), branch.BranchPath)
	}
	fmt.Println()
	printCommand("systemctl", "is-active", state.ServiceName+".service")
	printCommand("findmnt", state.MountPoint)
	printCommand("df", "-hT", state.MountPoint)
	return nil
}

func runInstall(reader *bufio.Reader) error {
	ok, err := confirm(reader, "确认安装 mergerfs fuse3 acl")
	if err != nil || !ok {
		return err
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
		home, err := promptDefault(reader, "没有自动找到 owner home，请输入 home 目录名", "1000")
		if err != nil {
			return OwnerCandidate{}, err
		}
		ownerName, err := promptDefault(reader, "请输入 owner 用户名", "")
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
		fmt.Printf("自动获取 owner: %s uid=%s gid=%s home=%s\n", emptyDash(owner.UserName), owner.UID, owner.GID, owner.HomeName)
		return owner, nil
	}
	labels := make([]string, 0, len(owners))
	for _, owner := range owners {
		labels = append(labels, fmt.Sprintf("home=%s owner=%s uid=%s gid=%s 命中=%d", owner.HomeName, emptyDash(owner.UserName), owner.UID, owner.GID, owner.Count))
	}
	chosen, err := chooseOne(reader, "选择 owner", labels)
	if err != nil {
		return OwnerCandidate{}, err
	}
	for i, label := range labels {
		if label == chosen {
			return owners[i], nil
		}
	}
	return OwnerCandidate{}, errors.New("未选择 owner")
}

func chooseVolumes(reader *bufio.Reader, volumes []Volume) ([]Volume, error) {
	labels := make([]string, 0, len(volumes))
	for _, vol := range volumes {
		labels = append(labels, volumeLabel(vol))
	}
	selectedLabels, err := chooseMany(reader, "选择参与 MFS 的卷", labels)
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
		fmt.Printf("自动获取 App 用户: %s\n", appUser)
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
		fmt.Printf("自动获取 App 用户: %s\n", appUser)
		return appUser, true
	}
	if len(app.UserCandidates) > 0 {
		return app.UserCandidates[0], false
	}
	return "", false
}

func maybeCustomizePlan(reader *bufio.Reader, plan SetupPlan) (SetupPlan, error) {
	ok, err := confirm(reader, "是否修改 appuser/name/path 等其他选项")
	if err != nil || !ok {
		return plan, err
	}
	appUser, err := promptDefault(reader, "App 用户", plan.AppUser)
	if err != nil {
		return plan, err
	}
	poolName, err := promptDefault(reader, "底层目录名", plan.PoolName)
	if err != nil {
		return plan, err
	}
	mountPoint, err := promptDefault(reader, "聚合入口路径", plan.MountPoint)
	if err != nil {
		return plan, err
	}
	return buildSetupPlan(plan.App, appUser, plan.Owner, poolName, mountPoint, plan.Volumes), nil
}

func buildSetupPlan(app AppConfig, appUser string, owner OwnerCandidate, poolName string, mountPoint string, volumes []Volume) SetupPlan {
	branches := make([]BranchState, 0, len(volumes))
	for _, vol := range volumes {
		branches = append(branches, BranchState{
			VolumePath: vol.Path,
			VolumeUUID: vol.UUID,
			BranchPath: filepath.Join(vol.Path, owner.HomeName, poolName),
		})
	}
	return SetupPlan{
		App: app, AppUser: appUser, Owner: owner, PoolName: poolName,
		MountPoint: mountPoint, Volumes: volumes, Branches: branches,
	}
}

func applySetup(plan SetupPlan) error {
	for _, branch := range plan.Branches {
		if err := os.MkdirAll(branch.BranchPath, 0775); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(plan.MountPoint, 0775); err != nil {
		return err
	}
	if err := chownIfKnown(plan.Owner, append(branchPaths(plan.Branches), plan.MountPoint)); err != nil {
		return err
	}
	state := AppState{
		AppID: plan.App.ID, AppLabel: plan.App.Label, AppUser: plan.AppUser,
		Owner: plan.Owner, PoolName: plan.PoolName, MountPoint: plan.MountPoint,
		ServiceName: plan.App.ServiceName, Branches: plan.Branches,
	}
	if err := applyACL(state, plan.AppUser); err != nil {
		return err
	}
	if err := ensureFuseAllowOther(); err != nil {
		return err
	}
	if err := writeState(state); err != nil {
		return err
	}
	if err := writeService(state); err != nil {
		return err
	}
	if err := runCommand("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := runCommand("systemctl", "enable", state.ServiceName+".service"); err != nil {
		return err
	}
	if err := runCommand("systemctl", "restart", state.ServiceName+".service"); err != nil {
		return err
	}
	return nil
}

func applyACL(state AppState, appUser string) error {
	if appUser == "" {
		return errors.New("App 用户为空，不能补 ACL")
	}
	for _, ancestor := range aclAncestors(state) {
		if err := runCommand("setfacl", "-m", "u:"+appUser+":--x", ancestor); err != nil {
			return err
		}
	}
	targets := append(branchPaths(state.Branches), state.MountPoint)
	for _, target := range targets {
		if err := runCommand("setfacl", "-Rm", "u:"+appUser+":rwx", target); err != nil {
			return err
		}
		if err := runCommand("setfacl", "-dm", "u:"+appUser+":rwx", target); err != nil {
			return err
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
	}
	add(filepath.Dir(state.MountPoint))
	return paths
}

func writeState(state AppState) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(stateDir, state.AppID+".json"), data, 0644)
}

func loadState(app AppConfig) (AppState, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, app.ID+".json"))
	if err != nil {
		return AppState{}, fmt.Errorf("没有找到 %s 状态文件，请先 set: %w", app.ID, err)
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
	fmt.Printf("缺少命令: %s\n", strings.Join(missing, ", "))
	ok, err := confirm(reader, "现在安装依赖")
	if err != nil || !ok {
		return err
	}
	return installDependencies()
}

func installDependencies() error {
	if _, err := exec.LookPath("apt"); err != nil {
		return errors.New("没有找到 apt，无法自动安装")
	}
	if err := runCommand("apt", "update"); err != nil {
		return err
	}
	return runCommand("apt", "install", "-y", "mergerfs", "fuse3", "acl")
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
		return errors.New("需要 root 权限，请用 sudo fnos-mfs 运行")
	}
	return nil
}

func runCommand(name string, args ...string) error {
	fmt.Printf("执行: %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func printCommand(name string, args ...string) {
	fmt.Printf("$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("命令失败: %v\n", err)
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

func printVolumes(volumes []Volume) {
	fmt.Println()
	fmt.Println("发现卷：")
	for _, vol := range volumes {
		fmt.Printf("- %s device=%s fstype=%s uuid=%s\n", vol.Path, emptyDash(vol.Device), emptyDash(vol.FSType), emptyDash(vol.UUID))
	}
	fmt.Println()
}

func printPlan(plan SetupPlan) {
	fmt.Println()
	fmt.Println("执行计划：")
	fmt.Printf("App: %s (%s)\n", plan.App.ID, plan.App.Label)
	fmt.Printf("App 用户: %s\n", emptyDash(plan.AppUser))
	fmt.Printf("Owner: %s uid=%s gid=%s home=%s\n", emptyDash(plan.Owner.UserName), plan.Owner.UID, plan.Owner.GID, plan.Owner.HomeName)
	fmt.Printf("底层目录名: %s\n", plan.PoolName)
	fmt.Printf("聚合入口: %s\n", plan.MountPoint)
	fmt.Printf("systemd: %s.service\n", plan.App.ServiceName)
	fmt.Println("分支目录：")
	for _, branch := range plan.Branches {
		fmt.Printf("- %s uuid=%s -> %s\n", branch.VolumePath, emptyDash(branch.VolumeUUID), branch.BranchPath)
	}
	fmt.Println()
}

func chooseOne(reader *bufio.Reader, title string, options []string) (string, error) {
	if len(options) == 0 {
		return "", errors.New("没有选项")
	}
	fmt.Println(title + ":")
	for i, option := range options {
		fmt.Printf("  %d. %s\n", i+1, option)
	}
	for {
		input, err := prompt(reader, "输入编号")
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(input)
		if err == nil && n >= 1 && n <= len(options) {
			fmt.Println()
			return options[n-1], nil
		}
		fmt.Println("编号无效")
	}
}

func chooseMany(reader *bufio.Reader, title string, options []string) ([]string, error) {
	if len(options) == 0 {
		return nil, errors.New("没有选项")
	}
	fmt.Println(title + ":")
	for i, option := range options {
		fmt.Printf("  %d. [ ] %s\n", i+1, option)
	}
	fmt.Println("输入示例：1,2,3 或 all")
	for {
		input, err := prompt(reader, "选择")
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
			return nil, fmt.Errorf("无效编号: %s", part)
		}
		idx := n - 1
		if !seen[idx] {
			seen[idx] = true
			indexes = append(indexes, idx)
		}
	}
	if len(indexes) == 0 {
		return nil, errors.New("至少选择一个")
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
	input, err := prompt(reader, label+"? 输入 yes 确认")
	if err != nil {
		return false, err
	}
	return input == "yes", nil
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
	return fmt.Sprintf("%s device=%s fstype=%s uuid=%s", vol.Path, emptyDash(vol.Device), emptyDash(vol.FSType), emptyDash(vol.UUID))
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
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
