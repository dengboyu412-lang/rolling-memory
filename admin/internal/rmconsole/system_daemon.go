// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/router/registry"

	"github.com/gin-gonic/gin"
)

// 系统管理面（守护进程状态与启停、内存上限、缓存清理）全部经产品 CLI 的正典子命令
// （`daemon ...` / `config set` / `engine invalidate`）执行：进程生命周期、配置写入校验与
// 守护通道核验都由引擎实现承担，控制台只做参数校验与结果透传。
const (
	// systemDaemonStatusTimeout 限定一次守护进程状态查询。
	systemDaemonStatusTimeout = 15 * time.Second
	// systemDaemonControlTimeout 限定一次启动/停止/重启（含有序停机与等待绑定）。
	systemDaemonControlTimeout = 45 * time.Second
	// systemConfigTimeout 限定一次配置读写。
	systemConfigTimeout = 15 * time.Second
	// systemCacheTimeout 限定一次缓存清理通知。
	systemCacheTimeout = 15 * time.Second
)

func init() {
	registry.RegisterAdmin(func(admin *gin.RouterGroup) {
		g := admin.Group("plugin/" + Name)
		g.Use(limitConsoleRequestBody())
		g.Use(consoleVaultScopeGuard())
		g.POST("/system/daemon", middleware.AdminAuth(), middleware.AdminPermission(), systemDaemonHandler)
		g.POST("/system/memory-limit", middleware.AdminAuth(), middleware.AdminPermission(), systemMemoryLimitHandler)
		g.POST("/system/cache", middleware.AdminAuth(), middleware.AdminPermission(), systemCacheHandler)
	})
}

type systemDaemonRequest struct {
	Action string `json:"action"` // status | start | stop | restart
}

// normalizeSystemDaemonAction 归一守护动作；空值默认 status，未知值拒绝。
func normalizeSystemDaemonAction(raw string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(raw))
	if action == "" {
		return "status", nil
	}
	switch action {
	case "status", "start", "stop", "restart":
		return action, nil
	}
	return "", errors.New("未知守护动作: " + raw + "；仅支持 status / start / stop / restart")
}

// systemDaemonHandler 查询或切换本地守护进程生命周期。
func systemDaemonHandler(c *gin.Context) {
	var req systemDaemonRequest
	if !bindJSON(c, &req) {
		return
	}
	action, err := normalizeSystemDaemonAction(req.Action)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
		return
	}
	timeout := systemDaemonStatusTimeout
	if action != "status" {
		timeout = systemDaemonControlTimeout
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	report, err := daemonReport(ctx, bin, resolveMemoryDBPath(home), action)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("守护命令执行失败: "+err.Error()))
		return
	}
	if action != "status" {
		invalidateEnvHealthCache(home)
	}
	httpx.Success(c, httpx.WithData(gin.H{"action": action, "report": report}))
}

// daemonReport 运行一次 `daemon <action> --json` 子进程并解析 report。
// HTTP 控制端与实时帧生产者共用同一份参数拼装与解析，避免两处契约漂移。
func daemonReport(ctx context.Context, bin, dbPath, action string) (map[string]any, error) {
	raw, err := systemCLIRunner(ctx, bin, []string{"daemon", action, "--db", dbPath, "--json"})
	if err != nil {
		return nil, err
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("解析守护报告失败: %w", err)
	}
	return report, nil
}

type systemMemoryLimitRequest struct {
	Value string `json:"value"`
}

// systemMemoryLimitHandler 写入守护进程内存上限（auto / off / 尺寸）。
// 取值校验与规范化由 `config set` 的配置层完成，控制台不自研第二套解析。
func systemMemoryLimitHandler(c *gin.Context) {
	var req systemMemoryLimitRequest
	if !bindJSON(c, &req) {
		return
	}
	value := strings.TrimSpace(req.Value)
	if value == "" {
		httpx.Fail(c, httpx.WithMessage("内存上限不能为空"))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), systemConfigTimeout)
	defer cancel()
	before, _ := readConfigMemoryLimit(ctx, bin)
	if _, err := systemCLIRunner(ctx, bin, []string{"config", "set", "daemon.memory_limit", value}); err != nil {
		httpx.Fail(c, httpx.WithMessage("写入内存上限失败: "+err.Error()))
		return
	}
	current, err := readConfigMemoryLimit(ctx, bin)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取配置失败: "+err.Error()))
		return
	}
	restartRequired := !strings.EqualFold(strings.TrimSpace(before), strings.TrimSpace(current))
	invalidateEnvHealthCache(home)
	httpx.Success(c, httpx.WithData(gin.H{
		"memory_limit":     current,
		"restart_required": restartRequired,
	}))
}

// readConfigMemoryLimit 读取配置 JSON 中的 daemon.memory_limit 规范值。
func readConfigMemoryLimit(ctx context.Context, bin string) (string, error) {
	raw, err := systemCLIRunner(ctx, bin, []string{"config", "show"})
	if err != nil {
		return "", err
	}
	var dump struct {
		Daemon struct {
			MemoryLimit string `json:"memory_limit"`
		} `json:"daemon"`
	}
	if err := json.Unmarshal(raw, &dump); err != nil {
		return "", err
	}
	return dump.Daemon.MemoryLimit, nil
}

// systemCacheHandler 丢弃守护进程内存快照与控制台读缓存中的可再生成部分。
func systemCacheHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	bin := resolveBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), systemCacheTimeout)
	defer cancel()
	if _, err := engineCommandRunner(ctx, bin, "invalidate", []string{"--db", resolveMemoryDBPath(home)}, []byte(`{}`)); err != nil {
		httpx.Fail(c, httpx.WithMessage("缓存清理失败: "+err.Error()))
		return
	}
	invalidateVaultReadCaches()
	invalidateEnvHealthCache(home)
	httpx.Success(c, httpx.WithData(gin.H{"invalidated": true}))
}

// systemCLIRunner 执行非 engine 通道的产品 CLI 子命令（daemon / config），
// 与 engineCommandRunner 同样有界捕获输出并剥离云端凭据；测试可替换。
var systemCLIRunner = runSystemCLI

func runSystemCLI(ctx context.Context, bin string, args []string) ([]byte, error) {
	if strings.TrimSpace(bin) == "" {
		return nil, errors.New("rolling-memory binary not found")
	}
	// 测试二进制只允许显式指定的产品二进制：单测不得意外执行开发机上安装的真实产品二进制。
	if adminTestBinary() && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BIN")) == "" {
		return nil, errors.New("system channel requires an explicit ROLLING_MEMORY_BIN under test binaries")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout := &engineCapture{limit: engineResponseLimit}
	stderr := &engineCapture{limit: engineStderrLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = stripCloudBrainEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.buffer.Bytes(), ctx.Err()
		}
		message := strings.TrimSpace(stderr.buffer.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.buffer.Bytes(), errors.New(message)
	}
	if stdout.overflow {
		return stdout.buffer.Bytes(), errors.New("system command output exceeds the size limit")
	}
	return stdout.buffer.Bytes(), nil
}
