package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestEmbeddedConfigIsValid(t *testing.T) {
	data, err := os.ReadFile("configs/apps.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	wantIDs := []string{"fnvideo", "fnmusic", "fnxunlei", "fnaria2"}
	var gotIDs []string
	for _, app := range cfg.Apps {
		gotIDs = append(gotIDs, app.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("app ids = %v, want %v", gotIDs, wantIDs)
	}
}

func withTempStateDir(t *testing.T) string {
	t.Helper()
	old := runtimeStateDir
	dir := t.TempDir()
	runtimeStateDir = dir
	t.Cleanup(func() {
		runtimeStateDir = old
	})
	return dir
}

func TestValidateConfigRejectsDuplicateAppID(t *testing.T) {
	cfg := Config{Apps: []AppConfig{
		{
			ID:              "fnvideo",
			Label:           "A",
			DefaultPoolName: ".media_pool",
			DefaultMountDir: "media",
			PathTemplate:    "{primary}/{home}/{mount_dir}",
			ServiceName:     "svc-a",
		},
		{
			ID:              "fnvideo",
			Label:           "B",
			DefaultPoolName: ".media_pool",
			DefaultMountDir: "media",
			PathTemplate:    "{primary}/{home}/{mount_dir}",
			ServiceName:     "svc-b",
		},
	}}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestParseCLIArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantEnglish bool
		wantHelp    bool
		wantErr     bool
	}{
		{name: "none"},
		{name: "english", args: []string{"-en"}, wantEnglish: true},
		{name: "help short", args: []string{"-h"}, wantHelp: true},
		{name: "help long", args: []string{"-help"}, wantHelp: true},
		{name: "english help", args: []string{"-en", "--help"}, wantEnglish: true, wantHelp: true},
		{name: "unknown", args: []string{"--bad"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLIArgs(tt.args)
			if tt.wantErr {
				var unknown unknownArgumentError
				if !errors.As(err, &unknown) {
					t.Fatalf("error = %v, want unknownArgumentError", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.English != tt.wantEnglish || got.Help != tt.wantHelp {
				t.Fatalf("options = %+v", got)
			}
		})
	}
}

func TestPrintUsageLanguage(t *testing.T) {
	var cn strings.Builder
	printUsage(&cn)
	if !strings.Contains(cn.String(), "用法:") || !strings.Contains(cn.String(), "sudo ./fnos-mfs -en") {
		t.Fatalf("Chinese usage missing expected text:\n%s", cn.String())
	}

	old := englishOutput
	englishOutput = true
	t.Cleanup(func() { englishOutput = old })

	var en strings.Builder
	printUsage(&en)
	for _, want := range []string{"Usage:", "Use English interactive text", "sudo ./fnos-mfs -en"} {
		if !strings.Contains(en.String(), want) {
			t.Fatalf("English usage missing %q:\n%s", want, en.String())
		}
	}
}

func TestChooseAppSupportsOther(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("5\n"))
	selection, err := chooseApp(reader, []AppConfig{
		{ID: "fnvideo", Label: "飞牛影视"},
		{ID: "fnmusic", Label: "飞牛音乐"},
		{ID: "fnxunlei", Label: "飞牛迅雷"},
		{ID: "fnaria2", Label: "Aria2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Other {
		t.Fatal("expected other selection")
	}
}

func TestPromptCustomApp(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("My App\n我的应用\napp.user\n.pool\n聚合\n"))
	app, err := promptCustomApp(reader)
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != "my-app" {
		t.Fatalf("id = %q, want my-app", app.ID)
	}
	if app.ServiceName != "fnos-mfs-my-app" {
		t.Fatalf("service = %q", app.ServiceName)
	}
	if !reflect.DeepEqual(app.UserCandidates, []string{"app.user"}) {
		t.Fatalf("user candidates = %v", app.UserCandidates)
	}
}

func TestPromptCustomAppUsesExistingStateDefaults(t *testing.T) {
	withTempStateDir(t)
	state := AppState{
		AppID:       "music",
		AppLabel:    "音乐",
		AppUser:     "XHOME",
		Owner:       OwnerCandidate{HomeName: "1000"},
		PoolName:    ".music_pool",
		MountPoint:  "/vol13/1000/音乐聚合",
		ServiceName: "fnos-mfs-music",
		Branches: []BranchState{
			{VolumePath: "/vol13", BranchPath: "/vol13/1000/.music_pool"},
		},
	}
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(strings.NewReader("music\n\n\n\n\n"))
	app, err := promptCustomApp(reader)
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != "music" || app.Label != "音乐" || app.ServiceName != "fnos-mfs-music" {
		t.Fatalf("unexpected app identity: %+v", app)
	}
	if app.DefaultPoolName != ".music_pool" {
		t.Fatalf("pool = %q, want .music_pool", app.DefaultPoolName)
	}
	if app.DefaultMountDir != "音乐聚合" {
		t.Fatalf("mount dir = %q, want 音乐聚合", app.DefaultMountDir)
	}
	if !reflect.DeepEqual(app.UserCandidates, []string{"XHOME"}) {
		t.Fatalf("user candidates = %v", app.UserCandidates)
	}
}

func TestAppendSavedCustomAppsAddsStateApps(t *testing.T) {
	withTempStateDir(t)
	states := []AppState{
		{
			AppID:       "music",
			AppLabel:    "音乐",
			AppUser:     "XHOME",
			Owner:       OwnerCandidate{HomeName: "1000"},
			PoolName:    ".music_pool",
			MountPoint:  "/vol13/1000/音乐聚合",
			ServiceName: "fnos-mfs-music",
		},
		{
			AppID:       "fnvideo",
			AppLabel:    "飞牛影视旧状态",
			AppUser:     "trim.media",
			PoolName:    ".media_pool",
			MountPoint:  "/vol1/1000/影视聚合",
			ServiceName: "fnos-mfs-fnvideo",
		},
	}
	for _, state := range states {
		if err := writeState(state); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(runtimeStateDir, "apps.json"), []byte(`{"apps":[]}`), 0644); err != nil {
		t.Fatal(err)
	}

	apps := appendSavedCustomApps([]AppConfig{
		{ID: "fnvideo", Label: "飞牛影视", DefaultPoolName: ".media_pool", DefaultMountDir: "影视聚合", PathTemplate: "{primary}/{home}/{mount_dir}", ServiceName: "fnos-mfs-fnvideo"},
	})
	if len(apps) != 2 {
		t.Fatalf("apps len = %d, want 2: %+v", len(apps), apps)
	}
	got := apps[1]
	if got.ID != "music" || got.Label != "音乐" || got.DefaultPoolName != ".music_pool" || got.DefaultMountDir != "音乐聚合" || got.ServiceName != "fnos-mfs-music" {
		t.Fatalf("saved custom app = %+v", got)
	}
	if !reflect.DeepEqual(got.UserCandidates, []string{"XHOME"}) {
		t.Fatalf("user candidates = %v", got.UserCandidates)
	}
}

func TestActionMenuReturnsAfterStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	reader := bufio.NewReader(strings.NewReader("4\n6\n"))
	app := AppConfig{ID: "fnvideo", Label: "飞牛影视", DefaultPoolName: ".media_pool", DefaultMountDir: "影视聚合"}
	if err := runActionMenu(reader, app); err != nil {
		t.Fatal(err)
	}
}

func TestActionMenuContinuesAfterActionError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	reader := bufio.NewReader(strings.NewReader("1\n6\n"))
	app := AppConfig{ID: "fnvideo", Label: "飞牛影视", DefaultPoolName: ".media_pool", DefaultMountDir: "影视聚合"}
	if err := runActionMenu(reader, app); err != nil {
		t.Fatal(err)
	}
}

func TestParseSelection(t *testing.T) {
	tests := []struct {
		name  string
		input string
		count int
		want  []int
	}{
		{name: "comma", input: "1,3", count: 4, want: []int{0, 2}},
		{name: "spaces", input: "3 1 2", count: 4, want: []int{0, 1, 2}},
		{name: "dedupe", input: "2,2,1", count: 4, want: []int{0, 1}},
		{name: "all", input: "ALL", count: 3, want: []int{0, 1, 2}},
		{name: "star", input: "*", count: 2, want: []int{0, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSelection(tt.input, tt.count)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSelection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseSelectionRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", "0", "4", "x"} {
		t.Run(input, func(t *testing.T) {
			if _, err := parseSelection(input, 3); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestChooseManyReturnsSelectedLabels(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("3,1\n"))
	got, err := chooseMany(reader, "选择卷", []string{"vol1", "vol2", "vol3"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vol1", "vol3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chooseMany() = %v, want %v", got, want)
	}
}

func TestDiscoverVolumesInSortsAndReadsMetadata(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"vol10", "vol2", "notvol", "vol1"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	fakeCommand := func(name string, args ...string) string {
		key := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(key, "-no SOURCE"):
			return "/dev/" + filepath.Base(args[len(args)-1])
		case strings.Contains(key, "-no FSTYPE"):
			return "ext4"
		case strings.Contains(key, "-no UUID"):
			return "uuid-" + filepath.Base(args[len(args)-1])
		default:
			return ""
		}
	}

	got, err := discoverVolumesIn(root, fakeCommand)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := []string{got[0].Name, got[1].Name, got[2].Name}
	wantNames := []string{"vol1", "vol2", "vol10"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("volume names = %v, want %v", gotNames, wantNames)
	}
	if got[1].Device != "/dev/vol2" || got[1].FSType != "ext4" || got[1].UUID != "uuid-vol2" {
		t.Fatalf("metadata for vol2 = %+v", got[1])
	}
}

func TestFindUUIDWithCommandFallsBackToLsblk(t *testing.T) {
	fakeCommand := func(name string, args ...string) string {
		if name == "lsblk" {
			return "lsblk-uuid"
		}
		return ""
	}
	got := findUUIDWithCommand("/vol1", "/dev/sda1", fakeCommand)
	if got != "lsblk-uuid" {
		t.Fatalf("uuid = %q, want lsblk-uuid", got)
	}
}

func TestHostStatusItems(t *testing.T) {
	lookPath := func(command string) (string, error) {
		if command == "apt" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + command, nil
	}
	items := hostStatusItems(lookPath, 1000)
	if len(items) != 3 {
		t.Fatalf("len = %d, want 3", len(items))
	}
	if items[0].State != statusWarn || items[1].State != statusOK || items[2].State != statusWarn {
		t.Fatalf("unexpected host status: %+v", items)
	}
}

func TestDiscoverOwnersCountsNumericHomeDirs(t *testing.T) {
	root := t.TempDir()
	vol1 := filepath.Join(root, "vol1")
	vol2 := filepath.Join(root, "vol2")
	for _, path := range []string{filepath.Join(vol1, "1000"), filepath.Join(vol2, "1000"), filepath.Join(vol2, "media")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	owners := discoverOwners([]Volume{{Path: vol1}, {Path: vol2}})
	if len(owners) != 1 {
		t.Fatalf("owners len = %d, want 1: %+v", len(owners), owners)
	}
	if owners[0].HomeName != "1000" || owners[0].Count != 2 {
		t.Fatalf("owner = %+v, want home 1000 count 2", owners[0])
	}
}

func TestRenderPathTemplate(t *testing.T) {
	got := renderPathTemplate("{primary}/{home}/{mount_dir}", "/vol2", "1000", "影视文件合集")
	want := "/vol2/1000/影视文件合集"
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestBuildSetupPlan(t *testing.T) {
	app := AppConfig{ID: "fnvideo", Label: "飞牛影视", ServiceName: "fnos-mfs-fnvideo"}
	owner := OwnerCandidate{HomeName: "1000", UID: "1000", GID: "1000", UserName: "XHOME"}
	volumes := []Volume{
		{Name: "vol2", Path: "/vol2", UUID: "u2"},
		{Name: "vol3", Path: "/vol3", UUID: "u3"},
	}
	plan := buildSetupPlan(app, "trim.media", owner, ".media_pool", "/vol2/1000/影视文件合集", volumes)
	if len(plan.Branches) != 2 {
		t.Fatalf("branches len = %d, want 2", len(plan.Branches))
	}
	if plan.Branches[0].BranchPath != "/vol2/1000/mfs_pools/.media_pool" {
		t.Fatalf("branch[0] = %q", plan.Branches[0].BranchPath)
	}
	if plan.MountPoint != "/vol2/1000/影视文件合集" {
		t.Fatalf("mount point = %q", plan.MountPoint)
	}
}

func TestBuildSetupPlanNormalizesPoolRootPrefix(t *testing.T) {
	app := AppConfig{ID: "fnvideo", Label: "飞牛影视", ServiceName: "fnos-mfs-fnvideo"}
	owner := OwnerCandidate{HomeName: "1000", UID: "1000", GID: "1000", UserName: "XHOME"}
	plan := buildSetupPlan(app, "trim.media", owner, "mfs_pools/.media_pool", "/vol1/1000/影视聚合", []Volume{{Name: "vol1", Path: "/vol1"}})
	if plan.PoolName != ".media_pool" {
		t.Fatalf("pool name = %q, want .media_pool", plan.PoolName)
	}
	if plan.Branches[0].BranchPath != "/vol1/1000/mfs_pools/.media_pool" {
		t.Fatalf("branch path = %q", plan.Branches[0].BranchPath)
	}
}

func TestSetupPlanChecksPassesValidPlan(t *testing.T) {
	plan := SetupPlan{
		AppUser:    "trim.media",
		Owner:      OwnerCandidate{HomeName: "1000", UID: "1000", GID: "1000"},
		PoolName:   ".media_pool",
		MountPoint: "/vol1/1000/影视聚合",
		Volumes: []Volume{
			{Name: "vol1", Path: "/vol1", Device: "/dev/sda1", FSType: "ext4", UUID: "u1", MountState: "mounted"},
			{Name: "vol2", Path: "/vol2", Device: "/dev/sdb1", FSType: "ext4", UUID: "u2", MountState: "mounted"},
		},
		Branches: []BranchState{
			{VolumePath: "/vol1", BranchPath: "/vol1/1000/mfs_pools/.media_pool"},
			{VolumePath: "/vol2", BranchPath: "/vol2/1000/mfs_pools/.media_pool"},
		},
	}
	command := func(name string, args ...string) string { return "" }
	lookup := func(username string) (*user.User, error) { return &user.User{Username: username}, nil }
	checks := setupPlanChecks(plan, command, lookup)
	if hasFailedStatus(checks) {
		t.Fatalf("unexpected failed checks: %+v", checks)
	}
}

func TestSetupPlanChecksFindsRiskyPlan(t *testing.T) {
	plan := SetupPlan{
		AppUser:    "missing.user",
		Owner:      OwnerCandidate{HomeName: "1000"},
		PoolName:   "bad/name",
		MountPoint: "relative/path",
		Volumes: []Volume{
			{Name: "vol1", Path: "/vol1", MountState: "unmounted"},
		},
		Branches: []BranchState{
			{VolumePath: "/vol1", BranchPath: "/vol1/1000/mfs_pools/bad/name"},
		},
	}
	command := func(name string, args ...string) string { return "" }
	lookup := func(username string) (*user.User, error) { return nil, os.ErrNotExist }
	checks := setupPlanChecks(plan, command, lookup)
	if !hasFailedStatus(checks) {
		t.Fatalf("expected failed checks: %+v", checks)
	}
	joined := statusItemsText(checks)
	for _, want := range []string{"至少选择两个卷", "卷未挂载", "底层目录名无效", "聚合入口路径必须是绝对路径", "App 用户不存在", "Owner UID/GID 缺失"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("checks missing %q: %s", want, joined)
		}
	}
}

func TestSetupPlanChecksRejectsMountPointContainingBranch(t *testing.T) {
	plan := SetupPlan{
		AppUser:    "trim.media",
		Owner:      OwnerCandidate{HomeName: "1000", UID: "1000", GID: "1000"},
		PoolName:   ".media_pool",
		MountPoint: "/vol1/1000",
		Volumes: []Volume{
			{Name: "vol1", Path: "/vol1", Device: "/dev/sda1", FSType: "ext4", UUID: "u1", MountState: "mounted"},
			{Name: "vol2", Path: "/vol2", Device: "/dev/sdb1", FSType: "ext4", UUID: "u2", MountState: "mounted"},
		},
		Branches: []BranchState{
			{VolumePath: "/vol1", BranchPath: "/vol1/1000/mfs_pools/.media_pool"},
			{VolumePath: "/vol2", BranchPath: "/vol2/1000/mfs_pools/.media_pool"},
		},
	}
	command := func(name string, args ...string) string { return "" }
	lookup := func(username string) (*user.User, error) { return &user.User{Username: username}, nil }
	checks := setupPlanChecks(plan, command, lookup)
	joined := statusItemsText(checks)
	if !strings.Contains(joined, "底层目录 /vol1/1000/mfs_pools/.media_pool 在挂载入口 /vol1/1000 内") {
		t.Fatalf("expected containing-path conflict: %+v", checks)
	}
}

func TestSetupPlanChecksRejectsMountedMountPoint(t *testing.T) {
	plan := SetupPlan{
		AppUser:    "trim.media",
		Owner:      OwnerCandidate{HomeName: "1000", UID: "1000", GID: "1000"},
		PoolName:   ".media_pool",
		MountPoint: "/vol1/1000/影视聚合",
		Volumes: []Volume{
			{Name: "vol1", Path: "/vol1", Device: "/dev/sda1", FSType: "ext4", UUID: "u1", MountState: "mounted"},
			{Name: "vol2", Path: "/vol2", Device: "/dev/sdb1", FSType: "ext4", UUID: "u2", MountState: "mounted"},
		},
		Branches: []BranchState{
			{VolumePath: "/vol1", BranchPath: "/vol1/1000/mfs_pools/.media_pool"},
			{VolumePath: "/vol2", BranchPath: "/vol2/1000/mfs_pools/.media_pool"},
		},
	}
	command := func(name string, args ...string) string {
		if name == "findmnt" && len(args) == 3 && args[2] == plan.MountPoint {
			return "fnos-mfs-fnvideo"
		}
		return ""
	}
	lookup := func(username string) (*user.User, error) { return &user.User{Username: username}, nil }
	checks := setupPlanChecks(plan, command, lookup)
	joined := statusItemsText(checks)
	if !strings.Contains(joined, "挂载入口已挂载") || !hasFailedStatus(checks) {
		t.Fatalf("expected mounted mount point failure: %+v", checks)
	}
}

func TestSetupPlanChecksAllowsCurrentServiceMountPoint(t *testing.T) {
	plan := SetupPlan{
		App:        AppConfig{ServiceName: "fnos-mfs-fnvideo"},
		AppUser:    "trim.media",
		Owner:      OwnerCandidate{HomeName: "1000", UID: "1000", GID: "1000"},
		PoolName:   ".media_pool",
		MountPoint: "/vol1/1000/影视聚合",
		Volumes: []Volume{
			{Name: "vol1", Path: "/vol1", Device: "/dev/sda1", FSType: "ext4", UUID: "u1", MountState: "mounted"},
			{Name: "vol2", Path: "/vol2", Device: "/dev/sdb1", FSType: "ext4", UUID: "u2", MountState: "mounted"},
		},
		Branches: []BranchState{
			{VolumePath: "/vol1", BranchPath: "/vol1/1000/mfs_pools/.media_pool"},
			{VolumePath: "/vol2", BranchPath: "/vol2/1000/mfs_pools/.media_pool"},
		},
	}
	command := func(name string, args ...string) string {
		if name == "findmnt" && len(args) == 3 && args[2] == plan.MountPoint {
			return "fnos-mfs-fnvideo"
		}
		return ""
	}
	lookup := func(username string) (*user.User, error) { return &user.User{Username: username}, nil }
	checks := setupPlanChecks(plan, command, lookup)
	joined := statusItemsText(checks)
	if !strings.Contains(joined, "挂载入口已由当前服务挂载") {
		t.Fatalf("expected current service mount warning: %+v", checks)
	}
	if hasFailedStatus(checks) {
		t.Fatalf("current service mount should not fail preflight: %+v", checks)
	}
}

func TestMaybeCustomizePlanKeepsDefaults(t *testing.T) {
	plan := SetupPlan{
		App:        AppConfig{ID: "fnvideo"},
		AppUser:    "trim.media",
		Owner:      OwnerCandidate{HomeName: "1000"},
		PoolName:   ".media_pool",
		MountPoint: "/vol1/1000/影视聚合",
		Volumes:    []Volume{{Path: "/vol1"}},
	}
	reader := bufio.NewReader(strings.NewReader("\n"))
	got, err := maybeCustomizePlan(reader, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("plan changed: %+v", got)
	}
}

func TestMaybeCustomizePlanAppliesOverrides(t *testing.T) {
	plan := SetupPlan{
		App:        AppConfig{ID: "fnvideo"},
		AppUser:    "trim.media",
		Owner:      OwnerCandidate{HomeName: "1000"},
		PoolName:   ".media_pool",
		MountPoint: "/vol1/1000/影视聚合",
		Volumes:    []Volume{{Path: "/vol1"}, {Path: "/vol2"}},
	}
	reader := bufio.NewReader(strings.NewReader("yes\ncustom.user\n.custom_pool\n/vol1/1000/custom\n"))
	got, err := maybeCustomizePlan(reader, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppUser != "custom.user" || got.PoolName != ".custom_pool" || got.MountPoint != "/vol1/1000/custom" {
		t.Fatalf("unexpected overrides: %+v", got)
	}
	if got.Branches[1].BranchPath != "/vol2/1000/mfs_pools/.custom_pool" {
		t.Fatalf("branch not rebuilt: %+v", got.Branches)
	}
}

func TestMigrateLegacyBranchesMovesOldPoolDirectory(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "vol1", "1000", ".media_pool")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "movie.mkv"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	plan := SetupPlan{
		Owner:    OwnerCandidate{HomeName: "1000"},
		PoolName: ".media_pool",
		Branches: []BranchState{
			{
				VolumePath: filepath.Join(root, "vol1"),
				BranchPath: filepath.Join(root, "vol1", "1000", "mfs_pools", ".media_pool"),
			},
		},
	}
	if err := migrateLegacyBranches(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy path still exists or stat failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(plan.Branches[0].BranchPath, "movie.mkv")); err != nil || string(data) != "data" {
		t.Fatalf("migrated file = %q, err=%v", string(data), err)
	}
}

func TestMigrateLegacyBranchesRejectsNonEmptyTarget(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "vol1", "1000", ".media_pool")
	target := filepath.Join(root, "vol1", "1000", "mfs_pools", ".media_pool")
	for _, path := range []string{legacy, target} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "existing.mkv"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	plan := SetupPlan{
		Owner:    OwnerCandidate{HomeName: "1000"},
		PoolName: ".media_pool",
		Branches: []BranchState{
			{VolumePath: filepath.Join(root, "vol1"), BranchPath: target},
		},
	}
	if err := migrateLegacyBranches(plan); err == nil {
		t.Fatal("expected non-empty target conflict")
	}
}

func TestAclAncestors(t *testing.T) {
	state := AppState{
		Owner:      OwnerCandidate{HomeName: "1000"},
		MountPoint: "/vol2/1000/影视文件合集",
		Branches: []BranchState{
			{VolumePath: "/vol2", BranchPath: "/vol2/1000/mfs_pools/.media_pool"},
			{VolumePath: "/vol3", BranchPath: "/vol3/1000/mfs_pools/.media_pool"},
		},
	}
	got := aclAncestors(state)
	want := []string{"/vol2", "/vol2/1000", "/vol2/1000/mfs_pools", "/vol3", "/vol3/1000", "/vol3/1000/mfs_pools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestors = %v, want %v", got, want)
	}
}

func TestWritePoolReadmes(t *testing.T) {
	root := t.TempDir()
	branch1 := filepath.Join(root, "vol1", "1000", "mfs_pools", ".media_pool")
	branch2 := filepath.Join(root, "vol2", "1000", "mfs_pools", ".media_pool")
	for _, branch := range []string{branch1, branch2} {
		if err := os.MkdirAll(branch, 0755); err != nil {
			t.Fatal(err)
		}
	}
	branches := []BranchState{
		{BranchPath: branch1},
		{BranchPath: branch2},
	}
	if err := writePoolReadmes(branches); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(root, "vol1", "1000", "mfs_pools", ".readme.txt")
	data, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"这是 fnos-mfs 创建和管理的底层池目录", "This directory is created and managed by fnos-mfs", "Do not delete"} {
		if !strings.Contains(text, want) {
			t.Fatalf("readme missing %q:\n%s", want, text)
		}
	}
}

func TestSystemdHelpers(t *testing.T) {
	if got := systemdPathValue(`/vol1/1000/My Media\A`); got != `/vol1/1000/My\x20Media\\A` {
		t.Fatalf("systemdPathValue = %q", got)
	}
	if got := systemdEnv("MFS_MOUNT", `/vol1/1000/"Media"\A`); got != `"MFS_MOUNT=/vol1/1000/\"Media\"\\A"` {
		t.Fatalf("systemdEnv = %q", got)
	}
}

func TestParseConfirm(t *testing.T) {
	for _, input := range []string{"yes", "YES", "y", " Y "} {
		ok, valid := parseConfirm(input)
		if !valid || !ok {
			t.Fatalf("expected %q to confirm", input)
		}
	}
	for _, input := range []string{"", "no", "NO", "n", " N "} {
		ok, valid := parseConfirm(input)
		if !valid || ok {
			t.Fatalf("expected %q to cancel", input)
		}
	}
	for _, input := range []string{"1", "maybe", "确认"} {
		_, valid := parseConfirm(input)
		if valid {
			t.Fatalf("expected %q to be invalid", input)
		}
	}
}

func TestConfirmRepromptsInvalidInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("maybe\nn\n"))
	ok, err := confirm(reader, "测试确认")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected confirm to cancel after n")
	}
}

func TestIsValidPoolName(t *testing.T) {
	for _, name := range []string{".media_pool", "media_pool"} {
		if !isValidPoolName(name) {
			t.Fatalf("expected valid pool name %q", name)
		}
	}
	for _, name := range []string{"", ".", "..", "bad/name", "bad:name"} {
		if isValidPoolName(name) {
			t.Fatalf("expected invalid pool name %q", name)
		}
	}
}

func TestStateStatusChecks(t *testing.T) {
	root := t.TempDir()
	branch := filepath.Join(root, "vol1", "1000", "mfs_pools", ".media_pool")
	mount := filepath.Join(root, "vol1", "1000", "影视聚合")
	if err := os.MkdirAll(branch, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mount, 0755); err != nil {
		t.Fatal(err)
	}
	state := AppState{
		MountPoint: mount,
		Branches: []BranchState{
			{VolumePath: filepath.Join(root, "vol1"), BranchPath: branch},
			{VolumePath: filepath.Join(root, "vol2"), BranchPath: filepath.Join(root, "vol2", "1000", "mfs_pools", ".media_pool")},
		},
	}
	checks := stateStatusChecks(state)
	joined := statusItemsText(checks)
	if !strings.Contains(joined, "底层目录不存在") || !strings.Contains(joined, "卷路径不存在") {
		t.Fatalf("expected missing path checks: %+v", checks)
	}
}

func statusItemsText(items []StatusItem) string {
	var parts []string
	for _, item := range items {
		parts = append(parts, item.Label+": "+item.Detail)
	}
	return strings.Join(parts, "\n")
}

func TestDependencyStatusItems(t *testing.T) {
	lookPath := func(command string) (string, error) {
		if command == "getfacl" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + command, nil
	}
	items := dependencyStatusItems(lookPath)
	if len(items) != 3 {
		t.Fatalf("len = %d, want 3", len(items))
	}
	if items[0].State != statusOK || items[1].State != statusOK {
		t.Fatalf("expected first two dependencies ok: %+v", items)
	}
	if items[2].State != statusFail || !strings.Contains(items[2].Detail, "getfacl") {
		t.Fatalf("expected acl failure for getfacl: %+v", items[2])
	}
}

func TestRepairSuggestions(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "root", err: errors.New("需要 root 权限，请用 sudo ./fnos-mfs 运行"), want: "sudo ./fnos-mfs"},
		{name: "volumes", err: errors.New("没有发现 /vol1 /vol2 这类卷"), want: "findmnt | grep /vol"},
		{name: "dependencies", err: errDependencyInstallDeclined, want: "apt install -y mergerfs fuse3 acl"},
		{name: "preflight", err: errSetupPreflightFailed, want: "预检结果"},
		{name: "app user", err: errors.New("App 用户为空，不能补 ACL"), want: "真实 App Linux 用户名"},
		{name: "acl", err: errors.New("setfacl -m u:trim.media:--x /vol1: exit status 1"), want: "id <appuser>"},
		{name: "systemd", err: errors.New("systemctl restart fnos-mfs-fnvideo.service: exit status 1"), want: "journalctl -u <service>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(repairSuggestions(tt.err), "\n")
			if !strings.Contains(got, tt.want) {
				t.Fatalf("suggestions = %q, want contains %q", got, tt.want)
			}
		})
	}
}

func TestRenderStatusItemNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := renderStatusItem(StatusItem{State: statusOK, Label: "mergerfs", Detail: "已安装"})
	want := "[OK] mergerfs: 已安装"
	if got != want {
		t.Fatalf("status item = %q, want %q", got, want)
	}
}

func TestRenderService(t *testing.T) {
	state := AppState{
		AppID:       "fnvideo",
		ServiceName: "fnos-mfs-fnvideo",
		MountPoint:  "/vol1/1000/My Media",
		Branches: []BranchState{
			{BranchPath: "/vol1/1000/mfs_pools/.media_pool"},
			{BranchPath: "/vol2/1000/mfs_pools/.media_pool"},
		},
	}
	service := renderService(state, "/usr/bin/mergerfs", "/usr/bin/fusermount3")
	required := []string{
		"Description=FNOS MFS fnvideo",
		`RequiresMountsFor=/vol1/1000/mfs_pools/.media_pool /vol2/1000/mfs_pools/.media_pool /vol1/1000/My\x20Media`,
		`Environment="MFS_OPTIONS=defaults,allow_other,use_ino,cache.files=off,category.create=mfs,moveonenospc=true,minfreespace=10G,fsname=fnos-mfs-fnvideo,umask=000"`,
		`Environment="MFS_BRANCHES=/vol1/1000/mfs_pools/.media_pool:/vol2/1000/mfs_pools/.media_pool"`,
		`Environment="MFS_MOUNT=/vol1/1000/My Media"`,
		`ExecStart=/bin/sh -c 'exec "$MFS_MERGERFS" -f -o "$MFS_OPTIONS" "$MFS_BRANCHES" "$MFS_MOUNT"'`,
	}
	for _, want := range required {
		if !strings.Contains(service, want) {
			t.Fatalf("service missing %q\n%s", want, service)
		}
	}
}

func TestNaturalVolLess(t *testing.T) {
	names := []string{"vol10", "vol2", "vol1"}
	sort.Slice(names, func(i, j int) bool {
		return naturalVolLess(names[i], names[j])
	})
	want := []string{"vol1", "vol2", "vol10"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID(" My App_01 ", "fallback"); got != "my-app-01" {
		t.Fatalf("sanitizeID = %q", got)
	}
	if got := sanitizeID("   ", "fallback"); got != "fallback" {
		t.Fatalf("sanitizeID fallback = %q", got)
	}
}
