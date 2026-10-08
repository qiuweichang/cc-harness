# cc-hardness

cc-hardness 是一个基于 Claude Code CLI 的多智能体软件研发编排平台。当前实现采用：

- Go 本地控制面，负责文件持久化、后续 DAG 调度和 Claude Code 进程管理。
- React + TypeScript Web 界面，视觉风格参考 Codex 桌面版的精致、简洁布局。
- 纯目录与 JSON/JSONL 持久化，不依赖 PostgreSQL、SQLite 或 Redis。

## 当前已实现

- 自动初始化 `data/catalog`、`data/config` 和 `data/tasks`。
- `GET /api/v1/health` 健康接口。
- `GET /api/v1/tasks` 扫描并查询本地任务目录。
- `GET /api/v1/tasks/{id}` 查询单个任务。
- `POST /api/v1/tasks` 创建任务及完整项目工作目录。
- 智能体、技能、规则和知识库的文件型 CRUD、版本、启禁用和关联查询。
- 启动时自动加载团队组建、项目经理、产品、架构、UI、前端、后端、测试和交付智能体。
- 自动加载需求、架构、接口数据、Stitch、前后端开发、测试、浏览器测试和交付技能。
- 新建任务、材料上传、团队生成、成员增减、版本和技能配置。
- 可编辑 DAG、节点增删改、依赖调整、节点运行、人工确认、失败与完成状态流转。
- Claude Code persistent `stream-json` 会话、成员对话、权限允许/拒绝、中断与停止。
- 补充需求、缺陷记录、文件列表、暂停恢复和最终验收。
- Codex 风格的完整 Web 管理与任务运行界面。

## 目录说明

```text
cmd/server/          Go 服务入口
internal/config/     本地配置
internal/httpapi/    REST API 与 Web 静态资源服务
internal/task/       任务模型、服务和文件仓储
web/                 React Web 应用
data/                默认持久化根目录
```

每个 `data/tasks/{task-id}` 文件夹都是一个完整任务，也是该任务的项目工作目录。平台运行状态位于其 `.cc-hardness/` 子目录中。

## 一键启动

Windows 下直接双击：

```text
start.bat
```

脚本会在首次启动时安装 Web 依赖，随后启动 Go 后端和 React 前端并打开浏览器。运行日志位于 `.runtime/logs/`。停止服务可双击 `stop.bat`。

## 本地开发

启动 Go 后端：

```powershell
go run ./cmd/server
```

启动 Web 开发服务：

```powershell
Set-Location web
npm install
npm run dev
```

Web 开发服务默认访问 `http://127.0.0.1:5172`，并将 `/api` 转发到 `http://127.0.0.1:8080`。

生产模式下先生成 `web/dist`，Go 服务会托管其中的静态文件。也可以通过以下环境变量修改默认配置：

- `CC_HARDNESS_ADDR`：HTTP 监听地址，默认 `127.0.0.1:8080`。
- `CC_HARDNESS_DATA_ROOT`：文件持久化根目录，默认 `./data`。
- `CC_HARDNESS_WEB_ROOT`：前端静态文件目录，默认 `./web/dist`。

完整设计见 [cc-hardness架构设计.md](./cc-hardness架构设计.md)。
