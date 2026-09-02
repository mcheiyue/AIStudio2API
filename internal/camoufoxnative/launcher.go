package camoufoxnative

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var bidiEndpointPattern = regexp.MustCompile(`ws://[^\s]+`)

const browserProcessCloseTimeout = 5 * time.Second

type browserProcessTerminator func(context.Context, *exec.Cmd) error

type browserProcess struct {
	command *exec.Cmd
	done    chan struct{}
	waitErr error
	profile string
	mu      sync.Mutex
	closed  bool
}

// launchBrowser 启动 Camoufox 并返回原生 WebDriver BiDi 端点
func launchBrowser(ctx context.Context, options Options, config map[string]any) (*browserProcess, string, error) {
	if _, err := os.Stat(options.ExecutablePath); err != nil {
		return nil, "", fmt.Errorf("Camoufox 不可用: %w", err)
	}
	environment, err := camoufoxEnvironment(config)
	if err != nil {
		return nil, "", err
	}
	profile, err := os.MkdirTemp("", "aistudio-camoufox-*")
	if err != nil {
		return nil, "", fmt.Errorf("创建 Camoufox profile: %w", err)
	}
	prefs, err := firefoxPreferences(options.Proxy, options.ProxyBypass)
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, "", err
	}
	if len(options.Extensions) > 0 {
		if err := installProfileExtensions(profile, options.Extensions, prefs); err != nil {
			_ = os.RemoveAll(profile)
			return nil, "", err
		}
	}
	if err := writeUserJS(profile, prefs); err != nil {
		_ = os.RemoveAll(profile)
		return nil, "", err
	}

	arguments := []string{"--remote-debugging-port=0"}
	if options.Headless {
		arguments = append(arguments, "-no-remote", "-headless")
	} else {
		arguments = append(arguments, "-wait-for-browser")
	}
	arguments = append(arguments, "-profile", profile)
	command := exec.Command(options.ExecutablePath, arguments...)
	command.Env = environment
	configureBrowserProcess(command, options.Headless)
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, "", err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, "", err
	}
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(profile)
		return nil, "", fmt.Errorf("启动 Camoufox: %w", err)
	}
	process := &browserProcess{
		command: command,
		done:    make(chan struct{}),
		profile: profile,
	}
	go func() {
		waitErr := command.Wait()
		process.mu.Lock()
		process.waitErr = waitErr
		process.mu.Unlock()
		close(process.done)
	}()
	endpoints := make(chan string, 2)
	go scanBrowserOutput(stdout, options.Log, endpoints)
	go scanBrowserOutput(stderr, options.Log, endpoints)
	timeout := options.ReadyTimeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case endpoint := <-endpoints:
		return process, normalizeBiDiEndpoint(endpoint), nil
	case <-process.done:
		process.mu.Lock()
		err := process.waitErr
		process.closed = true
		process.mu.Unlock()
		_ = os.RemoveAll(profile)
		if err == nil {
			err = errors.New("Camoufox 在报告 BiDi 端点前退出")
		}
		return nil, "", err
	case <-timer.C:
		_ = process.Close()
		return nil, "", fmt.Errorf("等待 Camoufox BiDi 端点超时: %s", timeout)
	case <-ctx.Done():
		_ = process.Close()
		return nil, "", ctx.Err()
	}
}

// Close 关闭 Camoufox 并删除隔离 profile
func (process *browserProcess) Close() error {
	return process.close(browserProcessCloseTimeout, terminateBrowserProcess)
}

func (process *browserProcess) close(timeout time.Duration, terminate browserProcessTerminator) error {
	if process == nil {
		return nil
	}
	process.mu.Lock()
	if process.closed {
		process.mu.Unlock()
		return nil
	}
	process.mu.Unlock()
	if timeout <= 0 {
		timeout = browserProcessCloseTimeout
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var closeErr error
	select {
	case <-process.done:
	default:
		closeErr = terminate(closeCtx, process.command)
		select {
		case <-process.done:
		case <-closeCtx.Done():
			closeErr = errors.Join(closeErr, fmt.Errorf("等待 Camoufox 进程退出: %w", closeCtx.Err()))
		}
	}
	closeErr = errors.Join(closeErr, removeProfile(process.profile))
	if closeErr != nil {
		return closeErr
	}
	process.mu.Lock()
	process.closed = true
	process.mu.Unlock()
	return nil
}

func removeProfile(profile string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.RemoveAll(profile)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func scanBrowserOutput(reader io.Reader, mirror io.Writer, endpoints chan<- string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if mirror != nil {
			_, _ = fmt.Fprintln(mirror, line)
		}
		if endpoint := bidiEndpointPattern.FindString(line); endpoint != "" {
			select {
			case endpoints <- endpoint:
			default:
			}
		}
	}
}

func normalizeBiDiEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err == nil && (parsed.Path == "" || parsed.Path == "/") {
		parsed.Path = "/session"
		return parsed.String()
	}
	return endpoint
}

func firefoxPreferences(proxyValue, bypass string) (map[string]any, error) {
	prefs := map[string]any{
		"remote.active-protocols":           1,
		"devtools.chrome.enabled":           true,
		"devtools.debugger.remote-enabled":  true,
		"browser.shell.checkDefaultBrowser": false,
	}
	proxyValue = strings.TrimSpace(proxyValue)
	if proxyValue == "" {
		return prefs, nil
	}
	parsed, err := url.Parse(proxyValue)
	if err != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("Camoufox 代理 URL 无效")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("Camoufox 原生代理暂不接受账号密码")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port <= 0 {
		return nil, fmt.Errorf("Camoufox 代理缺少有效端口")
	}
	prefs["network.proxy.type"] = 1
	prefs["network.proxy.no_proxies_on"] = bypass
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		prefs["network.proxy.http"] = parsed.Hostname()
		prefs["network.proxy.http_port"] = port
		prefs["network.proxy.ssl"] = parsed.Hostname()
		prefs["network.proxy.ssl_port"] = port
		prefs["network.proxy.share_proxy_settings"] = true
	case "socks4", "socks5":
		prefs["network.proxy.socks"] = parsed.Hostname()
		prefs["network.proxy.socks_port"] = port
		if strings.EqualFold(parsed.Scheme, "socks5") {
			prefs["network.proxy.socks_version"] = 5
			prefs["network.proxy.socks_remote_dns"] = true
		} else {
			prefs["network.proxy.socks_version"] = 4
		}
	default:
		return nil, fmt.Errorf("Camoufox 代理协议必须是 http、https、socks4 或 socks5")
	}
	return prefs, nil
}

func writeUserJS(profile string, prefs map[string]any) error {
	keys := make([]string, 0, len(prefs))
	for key := range prefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		value, err := firefoxPrefLiteral(prefs[key])
		if err != nil {
			return fmt.Errorf("Firefox pref %s: %w", key, err)
		}
		builder.WriteString("user_pref(")
		builder.WriteString(strconv.Quote(key))
		builder.WriteString(", ")
		builder.WriteString(value)
		builder.WriteString(");\n")
	}
	if err := os.WriteFile(filepath.Join(profile, "user.js"), []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("写入 Camoufox profile: %w", err)
	}
	return nil
}

func firefoxPrefLiteral(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	default:
		return "", fmt.Errorf("不支持 %T", value)
	}
}

// installProfileExtensions 把扩展打包成 XPI 放进 profile 的 extensions 目录。
// Firefox 启动时扫描该目录自动安装 <id>.xpi；Camoufox 属非品牌构建，允许关闭
// 签名校验，autoDisableScopes=0 避免外部安装的扩展被默认禁用。
// 注意：未打包文件夹放进 extensions 目录在 Firefox 152 上不可靠，必须 zip 成 XPI。
func installProfileExtensions(profile string, extensions []string, prefs map[string]any) error {
	if err := os.MkdirAll(filepath.Join(profile, "extensions"), 0o700); err != nil {
		return fmt.Errorf("创建扩展目录: %w", err)
	}
	for _, extension := range extensions {
		id, err := extensionID(extension)
		if err != nil {
			return err
		}
		xpiPath := filepath.Join(profile, "extensions", id+".xpi")
		if err := zipDirectory(extension, xpiPath); err != nil {
			return fmt.Errorf("打包扩展 %s: %w", id, err)
		}
	}
	prefs["xpinstall.signatures.required"] = false
	prefs["extensions.autoDisableScopes"] = 0
	return nil
}

// extensionID 从扩展目录的 manifest.json 提取 gecko id（profile 目录安装要求以 id 命名）
func extensionID(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", fmt.Errorf("读取扩展 manifest: %w", err)
	}
	var manifest struct {
		BrowserSpecificSettings *struct {
			Gecko *struct {
				ID string `json:"id"`
			} `json:"gecko"`
		} `json:"browser_specific_settings"`
		Applications *struct {
			Gecko *struct {
				ID string `json:"id"`
			} `json:"gecko"`
		} `json:"applications"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("解析扩展 manifest: %w", err)
	}
	if manifest.BrowserSpecificSettings != nil && manifest.BrowserSpecificSettings.Gecko != nil && manifest.BrowserSpecificSettings.Gecko.ID != "" {
		return manifest.BrowserSpecificSettings.Gecko.ID, nil
	}
	if manifest.Applications != nil && manifest.Applications.Gecko != nil && manifest.Applications.Gecko.ID != "" {
		return manifest.Applications.Gecko.ID, nil
	}
	return "", errors.New("扩展 manifest 缺少 browser_specific_settings.gecko.id")
}

func zipDirectory(dir, xpiPath string) error {
	file, err := os.Create(xpiPath)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(relative), Method: zip.Deflate}
		output, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	})
	if err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}
