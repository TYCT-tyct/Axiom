# Axiom

Axiom 是一个 LLM API 网关，对标 [OpenRouter](https://openrouter.ai)：用一套统一的 API 接入多家模型和上游。项目从零开发，取代旧项目 AxiomCode，不复用它的代码和文档。

- 参与开发：[AGENTS.md](AGENTS.md)（通用规则）和 [CONTRIBUTING.md](CONTRIBUTING.md)（本项目的补充）
- 文档地图：[docs/README.md](docs/README.md)
- 开发环境：Go（`GOTOOLCHAIN=auto` 会自动取 `go.mod` 里的版本）、[just](https://github.com/casey/just)、Python 3.11+、C 编译器（竞态检测要用），Windows 上还要 Git Bash；`just check` 跑完全部检查，命令见 [justfile](justfile)
- 本机部署：`just up` 构建程序并在 Docker 里启动，就绪检查通过才返回（目前程序只提供健康检查）；`just down` 停止。需要 Docker 和 Compose
