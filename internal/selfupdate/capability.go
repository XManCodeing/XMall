// Package selfupdate 实现后台一键升级：从 GitHub Release 拉取当前平台的二进制归档，
// 校验 sha256 后原子替换正在运行的可执行文件，通过 systemd 或 Linux exec 重启。
//
// 明确不支持容器部署。容器内替换 /app/xmall 只对当前容器生命周期有效，
// 一旦 docker restart / compose up / 宿主重启，进程又会回到镜像层里的旧二进制，
// 表现为「升级成功后过几天自己变回旧版」。容器的正确升级路径是拉新镜像重建容器，
// 因此这里探测到容器环境时直接阻断，由前端改为展示 compose 升级命令。
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/version"
)

// Deployment 当前进程的部署形态
type Deployment string

const (
	// DeploymentBinary 裸二进制部署（systemd / 手工启动）
	DeploymentBinary Deployment = "binary"
	// DeploymentContainer 容器部署（Docker / Podman / Kubernetes）
	DeploymentContainer Deployment = "container"
)

// BlockReason 一键升级不可用的原因码。前端据此渲染对应的手动升级指引，
// 不直接透传后端文案，保证 i18n 由前端统一处理。
type BlockReason string

const (
	BlockNone           BlockReason = ""
	BlockUpdateSource   BlockReason = "update_source_unavailable"
	BlockContainer      BlockReason = "container"
	BlockUnsupportedOS  BlockReason = "unsupported_os"
	BlockSourceBuild    BlockReason = "source_build"
	BlockDirNotWritable BlockReason = "dir_not_writable"
	BlockExecNotFound   BlockReason = "exec_not_found"
)

// Supervisor 进程守护方式，决定替换二进制后能否自动重启
type Supervisor string

const (
	// SupervisorSystemd 由 systemd 托管，进程退出后靠 Restart= 策略拉起
	SupervisorSystemd Supervisor = "systemd"
	// SupervisorNone 无 systemd；Linux 可经预检使用 exec，但没有新增进程守护。
	SupervisorNone Supervisor = "none"
)

// Capability 一键升级能力探测结果，直接序列化给后台前端
type Capability struct {
	// CanUpdate 是否允许下载并替换二进制
	CanUpdate bool `json:"can_update"`
	// CanRestart 替换完成后能否由后端自动重启进程；false 时需用户手动重启
	CanRestart         bool          `json:"can_restart"`
	RestartMethod      RestartMethod `json:"restart_method"`
	RestartBlockReason string        `json:"restart_block_reason,omitempty"`
	// BlockReason CanUpdate 为 false 时的原因码
	BlockReason BlockReason `json:"block_reason,omitempty"`
	Deployment  Deployment  `json:"deployment"`
	Supervisor  Supervisor  `json:"supervisor"`
	BuildType   string      `json:"build_type"`
	// RestartPolicy systemd unit 的 Restart= 取值（查不到时为空），
	// 决定 CanRestart —— 排障时能直接看出是策略不支持还是没探测到 unit
	RestartPolicy string `json:"restart_policy,omitempty"`
	// ExecPath 当前可执行文件的真实路径（已解析软链），便于用户核对升级目标
	ExecPath  string `json:"exec_path,omitempty"`
	Platform  string `json:"platform"`
	AssetName string `json:"asset_name,omitempty"`
	// HasBackup 是否存在可回滚的上一版本备份
	HasBackup bool `json:"has_backup"`
	// Backup 备份来源、迁移与启动状态。无备份、或元数据缺失/损坏时为 nil。
	Backup *BackupInfo `json:"backup,omitempty"`
	// RollbackUnsafe 回滚是否需要用户确认风险后强制执行。
	// 迁移已开始、完整启动过、或元数据不可信而无法判断时都为 true —— 后者同样危险，
	// 从旧版本升上来的第一次就属于这种情况。前端据此决定是否弹风险确认并带 force。
	RollbackUnsafe bool `json:"rollback_unsafe"`
}

// Detect 探测当前运行环境是否支持一键升级。
// 判定顺序按「越根本越先」排列，第一个不满足的条件即为阻断原因。
func Detect() (c Capability) {
	defer func() {
		if !c.CanRestart && c.RestartBlockReason == "" {
			c.RestartBlockReason = string(c.BlockReason)
		}
	}()
	c = Capability{
		RestartMethod: RestartNone,
		Deployment:    DeploymentBinary,
		Supervisor:    SupervisorNone,
		BuildType:     version.BuildType,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
	}

	if InContainer() {
		c.Deployment = DeploymentContainer
		c.BlockReason = BlockContainer
		return c
	}

	// Windows 上正在运行的 exe 虽可重命名，但没有等价的守护/重启约定，
	// 且不是本项目的目标部署环境，直接阻断而不是留一条没验证过的路径。
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		c.BlockReason = BlockUnsupportedOS
		return c
	}

	// 本地 go build 的版本号是占位值 v1.1.0，任何线上发行版都会被判定为「有新版本」，
	// 放开升级等于把开发者自己编译的二进制悄悄换掉。
	if !version.IsReleaseBuild() {
		c.BlockReason = BlockSourceBuild
		return c
	}

	execPath, err := ExecutablePath()
	if err != nil {
		c.BlockReason = BlockExecNotFound
		return c
	}
	c.ExecPath = execPath

	// 替换走 rename，需要的是二进制所在目录的写权限，而不是文件本身的写权限。
	if !dirWritable(filepath.Dir(execPath)) {
		c.BlockReason = BlockDirNotWritable
		return c
	}

	c.CanUpdate = true
	c.AssetName = PlatformAssetSuffix()
	c.Supervisor = detectSupervisor()
	// Direct systemd supervision requires a policy that relaunches our exit.
	// Children of a panel service instead need exec; inherited environment alone
	// cannot distinguish these cases. Unknown ownership stays blocked.
	configureRestart(&c, runtime.GOOS, os.Getpid(), systemdRestartConfig, preflightExec)
	if _, err := os.Stat(backupPath(execPath)); err == nil {
		c.HasBackup = true
		info, known, err := ReadBackupInfo(execPath)
		if err == nil && known {
			c.Backup = &info
			c.RollbackUnsafe = rollbackUnsafe(info, true)
		} else {
			// 元数据缺失或损坏：无法证明新版本还没跑过，按不安全处理，
			// 让前端弹风险确认并带 force。宁可多问一次，也不能默默放行。
			c.RollbackUnsafe = true
		}
	}
	return c
}

func configureRestart(c *Capability, goos string, pid int, systemd func() (unitRestartConfig, bool), preflight func(string) error) {
	c.RestartMethod = RestartNone
	c.CanRestart = false
	c.RestartBlockReason = ""
	c.RestartPolicy = ""
	if c.Supervisor == SupervisorSystemd {
		cfg, ok := systemd()
		// Environment and cgroup membership also belong to panel-launched children.
		// An active aaPanel forking unit can explicitly report MainPID=0: it does
		// not track a main process. Only this verified case may use exec without
		// a positive MainPID; unknown ownership remains blocked.
		if !ok || pid <= 0 || cfg.MainPID < 0 || (cfg.MainPID == 0 && !cfg.UntrackedPanel) {
			c.RestartBlockReason = "systemd_policy_unknown"
			return
		}
		if cfg.MainPID == pid {
			c.RestartPolicy = cfg.Restart
			if !restartConfigRelaunches(cfg) {
				c.RestartBlockReason = "systemd_policy_disabled"
				return
			}
			c.RestartMethod, c.CanRestart = RestartSystemd, true
			return
		}
		// Exiting this child would not restart the enclosing panel service,
		// even with Restart=always. Replace this process in place instead.
		c.Supervisor = SupervisorNone
	}
	if goos != "linux" {
		c.RestartBlockReason = "unsupported_os"
		return
	}
	if err := preflight(c.ExecPath); err != nil {
		c.RestartBlockReason = "exec_preflight_failed"
		return
	}
	c.RestartMethod, c.CanRestart = RestartExec, true
}

// ExecutablePath 返回当前可执行文件解析软链后的绝对路径。
// 必须解析软链：常见部署会用 /usr/local/bin/xmall -> /opt/xmall/xmall，
// 直接替换软链会把链接本身变成普通文件。
// startupExecutable is resolved during package initialization, before any update can
// rename the running inode to .backup (Linux os.Executable follows that rename).
var startupExecutable, startupExecutableErr = resolveExecutablePath()

func ExecutablePath() (string, error) {
	return startupExecutable, startupExecutableErr
}

func resolveExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return resolved, nil
}

// InContainer 判断进程是否运行在容器内。
// 覆盖 Docker、Podman、Kubernetes 三种常见形态；任一命中即认为是容器。
func InContainer() bool {
	// Docker 官方镜像运行时一定存在
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Podman / CRI-O
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	// cgroup v1 下 PID 1 的 cgroup 路径带容器运行时标记；
	// cgroup v2 纯净环境下可能只有 "0::/"，此时靠上面几项兜底。
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		for _, marker := range []string{"/docker/", "/docker-", "containerd", "kubepods", "/lxc/", "libpod"} {
			if strings.Contains(content, marker) {
				return true
			}
		}
	}
	return false
}

// detectSupervisor identifies possible systemd ownership, not direct supervision.
// Panel children inherit both the environment and cgroup; configureRestart checks
// the unit's MainPID before choosing an exit-based restart.
func detectSupervisor() Supervisor {
	if systemdUnit() != "" || os.Getenv("INVOCATION_ID") != "" || os.Getenv("JOURNAL_STREAM") != "" {
		return SupervisorSystemd
	}
	return SupervisorNone
}

// systemctlTimeout 查询 unit 属性的超时。systemctl show 走本地 D-Bus，正常在毫秒级；
// 给 2 秒是为了在 D-Bus 异常时不把能力探测接口拖住。
const systemctlTimeout = 2 * time.Second

// systemdUnit 从 cgroup 路径解析当前进程所属的 systemd service 名。
//
//	cgroup v2: "0::/system.slice/xmall.service"
//	cgroup v1: "1:name=systemd:/system.slice/xmall.service"
//
// 用 cgroup 而不是猜固定名字：unit 名由用户自己定，文档里叫 xmall.service，
// 实际部署可能是任意名称。
func systemdUnit() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return systemdUnitFromCgroup(string(data))
}

func systemdUnitFromCgroup(data string) string {
	return systemdServiceFromCgroup(data).name
}

type systemdService struct {
	name string
	user bool
	uid  int
}

func systemdServiceFromCgroup(data string) systemdService {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || (fields[1] != "" && !strings.Contains(","+fields[1]+",", ",name=systemd,")) {
			continue
		}
		// The innermost service owns the process (e.g. a user service below
		// user@1000.service), not the first enclosing manager service.
		segments := strings.Split(fields[2], "/")
		for i := len(segments) - 1; i >= 0; i-- {
			if !strings.HasSuffix(segments[i], ".service") {
				continue
			}
			service := systemdService{name: segments[i]}
			for _, parent := range segments[:i] {
				if strings.HasPrefix(parent, "user@") && strings.HasSuffix(parent, ".service") {
					uid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(parent, "user@"), ".service"))
					if err != nil || uid < 0 {
						return systemdService{}
					}
					service.user, service.uid = true, uid
				}
			}
			return service
		}
	}
	return systemdService{}
}

// unitRestartConfig 决定「以 RestartExitCode 退出后会不会被拉起」所需的全部 unit 配置。
type unitRestartConfig struct {
	// MainPID distinguishes the service itself from inherited panel children.
	MainPID int
	// UntrackedPanel is set only after querying the known system panel unit.
	UntrackedPanel bool
	Type           string
	LoadState      string
	ActiveState    string
	// Restart unit 的 Restart= 取值
	Restart string
	// SuccessExitStatus 声明为「成功」的额外退出码集合
	SuccessExitStatus map[int]bool
	// RestartPreventExitStatus 明确禁止触发重启的退出码集合
	RestartPreventExitStatus map[int]bool
}

// systemdRestartConfig queries ownership and exit-based restart policy together.
// 查询失败时返回 (零值, false) —— 调用方必须按「无法证明能重启」处理。
func systemdRestartConfig() (unitRestartConfig, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return unitRestartConfig{}, false
	}
	return querySystemdRestartConfig(systemdServiceFromCgroup(string(data)), os.Geteuid(), func(args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
		defer cancel()
		return exec.CommandContext(ctx, "systemctl", args...).Output()
	})
}

func querySystemdRestartConfig(service systemdService, uid int, query func([]string) ([]byte, error)) (unitRestartConfig, bool) {
	// Never query another manager's same-named unit. If credentials changed
	// since launch, we cannot safely address the owning user manager.
	if service.name == "" || (service.user && service.uid != uid) {
		return unitRestartConfig{}, false
	}
	args := []string{"show", "--property=MainPID", "--property=Type", "--property=LoadState", "--property=ActiveState", "--property=Restart", "--property=SuccessExitStatus", "--property=RestartPreventExitStatus"}
	if service.user {
		args = append(args, "--user")
	} else {
		args = append(args, "--system")
	}
	args = append(args, "--", service.name)
	out, err := query(args)
	if err != nil {
		// Do not fall back to the system manager when the user bus is unavailable.
		return unitRestartConfig{}, false
	}
	cfg, ok := parseSystemdRestartConfig(string(out))
	if !ok {
		return unitRestartConfig{}, false
	}
	// aaPanel starts detached Go projects inside its system service cgroup,
	// sometimes without tracking any MainPID. Do not generalize this exception
	// to arbitrary units or a same-named user service.
	cfg.UntrackedPanel = !service.user && service.name == "btpanel.service" &&
		cfg.MainPID == 0 && cfg.Type == "forking" &&
		cfg.LoadState == "loaded" && cfg.ActiveState == "active"
	return cfg, true
}

func parseSystemdRestartConfig(out string) (unitRestartConfig, bool) {
	cfg := unitRestartConfig{
		SuccessExitStatus:        map[int]bool{},
		RestartPreventExitStatus: map[int]bool{},
	}
	mainPIDSeen := false
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "MainPID":
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || pid < 0 {
				return unitRestartConfig{}, false
			}
			cfg.MainPID = pid
			mainPIDSeen = true
		case "Type":
			cfg.Type = strings.TrimSpace(value)
		case "LoadState":
			cfg.LoadState = strings.TrimSpace(value)
		case "ActiveState":
			cfg.ActiveState = strings.TrimSpace(value)
		case "Restart":
			cfg.Restart = strings.TrimSpace(value)
		case "SuccessExitStatus":
			cfg.SuccessExitStatus = parseExitStatusSet(value)
		case "RestartPreventExitStatus":
			cfg.RestartPreventExitStatus = parseExitStatusSet(value)
		}
	}
	if cfg.Restart == "" || !mainPIDSeen {
		// Missing MainPID is not the same as an explicitly reported zero.
		// configureRestart still rejects zero unless this is a verified panel.
		return unitRestartConfig{}, false
	}
	return cfg, true
}

// parseExitStatusSet 解析 systemd 的退出码列表。取值形如 "70 75" 或 "1 SIGTERM"，
// 信号名对本判定无意义（我们只关心退出码），忽略即可。
func parseExitStatusSet(value string) map[int]bool {
	set := map[int]bool{}
	for _, field := range strings.Fields(value) {
		if n, err := strconv.Atoi(field); err == nil {
			set[n] = true
		}
	}
	return set
}

// restartConfigRelaunches 报告以 RestartExitCode 退出后 systemd 会不会重新拉起本服务。
//
// 判定顺序与 systemd 一致：
//  1. RestartPreventExitStatus 命中则一票否决，压过任何 Restart= 策略
//  2. SuccessExitStatus 决定这次退出算「成功」还是「失败」
//  3. 再按 Restart= 策略判断该类退出会不会触发重启
//
// on-abnormal / on-abort / on-watchdog 只认信号、超时与 watchdog，从不看退出码；
// Restart= 的默认值是 no，也就是什么都不做。
func restartConfigRelaunches(cfg unitRestartConfig) bool {
	if cfg.RestartPreventExitStatus[RestartExitCode] {
		return false
	}
	exitIsSuccess := cfg.SuccessExitStatus[RestartExitCode]

	switch cfg.Restart {
	case "always":
		return true
	case "on-failure":
		return !exitIsSuccess
	case "on-success":
		return exitIsSuccess
	default:
		return false
	}
}

// dirWritable 通过实际创建临时文件判断目录可写。
// 不用 unix.Access：进程可能以非 root 运行且属主/属组与 stat 位的组合难以准确推断，
// 直接试一次最可靠。
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".xmall-update-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// PlatformAssetSuffix 返回当前平台对应的 goreleaser 归档文件名后缀。
// 与 .goreleaser.yaml 的 name_template 保持一致：
//
//	{{ProjectName}}_{{.Tag}}_{{title .Os}}_{{x86_64|i386|其它原样}}
//
// 例：xmall_v1.5.0_Linux_x86_64.tar.gz
func PlatformAssetSuffix() string {
	osPart := strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]

	archPart := runtime.GOARCH
	switch runtime.GOARCH {
	case "amd64":
		archPart = "x86_64"
	case "386":
		archPart = "i386"
	}

	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("_%s_%s%s", osPart, archPart, ext)
}

// backupPath 上一版本二进制的备份路径
func backupPath(execPath string) string {
	return execPath + ".backup"
}
