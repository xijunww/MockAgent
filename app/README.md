# MockAgent App

这里是 MockAgent 的 Wails v2 应用子模块。根目录的 [README.md](../README.md) 说明了完整的配置、功能和运行方式。

## 常用命令

```pwsh
# 首次检出或清理后，先生成 frontend/dist 供 Go embed 使用
cd frontend
npm install
npm run build
cd ..

# 后端单元测试
go test ./...

# 开发模式
wails dev

# 生产构建
wails build
```

`frontend/wailsjs/` 与 `frontend/dist/` 都是生成目录；`wails dev` / `wails build` 会按需生成。
