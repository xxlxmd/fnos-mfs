package main

import (
	"bufio"
	"encoding/json"
	"os"
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
	if plan.Branches[0].BranchPath != "/vol2/1000/.media_pool" {
		t.Fatalf("branch[0] = %q", plan.Branches[0].BranchPath)
	}
	if plan.MountPoint != "/vol2/1000/影视文件合集" {
		t.Fatalf("mount point = %q", plan.MountPoint)
	}
}

func TestAclAncestors(t *testing.T) {
	state := AppState{
		Owner:      OwnerCandidate{HomeName: "1000"},
		MountPoint: "/vol2/1000/影视文件合集",
		Branches: []BranchState{
			{VolumePath: "/vol2", BranchPath: "/vol2/1000/.media_pool"},
			{VolumePath: "/vol3", BranchPath: "/vol3/1000/.media_pool"},
		},
	}
	got := aclAncestors(state)
	want := []string{"/vol2", "/vol2/1000", "/vol3", "/vol3/1000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestors = %v, want %v", got, want)
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

func TestRenderService(t *testing.T) {
	state := AppState{
		AppID:       "fnvideo",
		ServiceName: "fnos-mfs-fnvideo",
		MountPoint:  "/vol1/1000/My Media",
		Branches: []BranchState{
			{BranchPath: "/vol1/1000/.media_pool"},
			{BranchPath: "/vol2/1000/.media_pool"},
		},
	}
	service := renderService(state, "/usr/bin/mergerfs", "/usr/bin/fusermount3")
	required := []string{
		"Description=FNOS MFS fnvideo",
		`RequiresMountsFor=/vol1/1000/.media_pool /vol2/1000/.media_pool /vol1/1000/My\x20Media`,
		`Environment="MFS_OPTIONS=defaults,allow_other,use_ino,cache.files=off,category.create=mfs,moveonenospc=true,minfreespace=10G,fsname=fnos-mfs-fnvideo,umask=000"`,
		`Environment="MFS_BRANCHES=/vol1/1000/.media_pool:/vol2/1000/.media_pool"`,
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
