package main

import (
	"context"
)

// App 是 Wails 后端协调器；具体业务逻辑（hotkey/recorder/asr/llm）将在后续任务中接入。
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup 由 Wails 在主窗口创建之后调用，用于保存运行时 ctx。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// beforeClose 在主窗口关闭前被调用；返回 true 阻止真正关闭，并在后续任务中改为隐藏窗口。
// 当前为占位实现，先返回 false，让默认关闭行为生效（任务 11.10 改为隐藏）。
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	_ = ctx
	return false
}
