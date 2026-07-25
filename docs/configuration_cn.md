# 配置说明

[← 返回 README](../README_CN.md)

## 配置优先级

```text
环境变量 > config.yaml > 代码默认值
```

### 配置文件位置与 Railway 持久化

本地直接运行时继续使用仓库根目录的 `config.yaml`。当设置了
`ACCOUNTS_DIR`（Docker / Railway 默认是 `/app/accounts`）时，程序会首次启动
时把默认配置复制到 `${ACCOUNTS_DIR}/.notion-manager-config.yaml`，之后
Dashboard 修改的设置都会写入这个持久卷文件。这样 Railway 休眠、重新创建
容器或拉取新镜像后，设置仍会保留。也可以用 `CONFIG_PATH` 指定一个明确的
配置文件路径；它优先于自动路径。

Railway 需要同时满足两点：`ACCOUNTS_DIR=/app/accounts`，并且确实挂载了
`/app/accounts` Volume。只设置环境变量而没有挂载 Volume，容器重建后文件仍
会消失。环境变量（例如 `USE_NOTION_PERSONAL_INSTRUCTIONS`）优先级最高，
会覆盖 Dashboard 保存的同名设置。

## Server

| 配置项 | 仓库样例 | 代码默认 | 说明 |
| --- | --- | --- | --- |
| `server.port` | `3000` | `8081` | 监听端口 |
| `server.accounts_dir` | `accounts` | `accounts` | 账号 JSON 目录；同时存放 `.register_history.json` / `.token_stats.json` / `.register_inputs/` |
| `server.token_file` | `token.txt` | `token.txt` | 单账号回退文件；容器部署且使用 `ACCOUNTS_DIR` 时会自动放入该持久卷 |
| `server.api_key` | 空 | 自动生成 | `/v1/messages` 鉴权 |
| `server.admin_password` | 空 | 自动生成 | 首次启动后哈希回写；环境变量 `ADMIN_PASSWORD` 可覆盖 |
| `server.log_file` | `server.stderr.log` | 空（stderr） | `log.Printf` 全部追加到该文件 |
| `server.debug_logging` | `true` | `true` | 高频调试日志 |
| `server.api_log_input` | `true` | `false` | 记录入站 `/v1/messages` 请求体 |
| `server.api_log_output` | `true` | `false` | 记录返回给 API 客户端的响应 |
| `server.notion_log_request` | `true` | `false` | 记录发送给 Notion 的 `runInferenceTranscript` 请求体 |
| `server.notion_log_response` | `true` | `false` | 记录 Notion 原始推理响应 |
| `server.dump_api_input` | `false` | `false` | 每个请求把系统提示词 + 工具 schema 落盘（用于 Claude Code 调试） |

## Proxy / 运行行为

| 配置项 | 仓库样例 | 代码默认 | 说明 |
| --- | --- | --- | --- |
| `proxy.notion_api_base` | `https://www.notion.so/api/v3` | 同左 | Notion API 入口 |
| `proxy.client_version` | `23.13.20260313.1423` | 同左 | `x-notion-client-version` 请求头 |
| `proxy.default_model` | `claude-opus-4.6` | `claude-opus-4.6` | 请求未传 `model` 时的回退 |
| `proxy.disable_notion_prompt` | `true` | `false` | 关闭 Notion 内置 ~33k 系统提示，节省输入 token |
| `proxy.use_client_system_prompt` | `true` | `true` | 是否带上客户端传来的 System Prompt；与官网个人指令互相独立 |
| `proxy.use_notion_personal_instructions` | `false` | `false` | 使用所选账号官网默认 Notion Agent 的个人指令页面；项目只读取页面 ID、不读取正文 |
| `proxy.check_personal_instructions_on_import` | `false` | `false` | 导入 token 时检测默认 Agent 是否已绑定个人指令；Dashboard 可选择全部导入或只导入已设置账号 |
| `proxy.enable_tool_bridge` | `true` | `true` | 把外部 Tools / 函数调用转换成 Claude Code 等客户端所需的兼容格式 |
| `proxy.enable_web_search` | `true` | `true` | 联网搜索全局开关（可被请求头覆盖） |
| `proxy.enable_workspace_search` | `false` | `false` | 工作区搜索全局开关（可被请求头覆盖） |
| `proxy.ask_mode_default` | 未设置 | `false` | 为 `true` 时所有对话默认走 `useReadOnlyMode=true`（即 Notion 前端的 “Ask” 开关）；请求级 `-ask` 后缀仍可覆盖 |
| `proxy.notion_proxy` | 空 | 空 | 上游代理（`http`/`https`/`socks5`/`socks5h`），作用于**全部** Notion 出站连接，包括批量注册、`/ai`、`/v1/messages` |

## Timeouts / Refresh

| 配置项 | 仓库样例 | 代码默认 | 说明 |
| --- | --- | --- | --- |
| `timeouts.inference_timeout` | `300` | `300` | 普通推理超时（秒） |
| `timeouts.research_timeout` | 未设置 | `360` | 研究模式超时（秒），作用于 `researcher` / `fast-researcher` |
| `timeouts.api_timeout` | `30` | `30` | 配额 / 模型 REST 超时（秒） |
| `timeouts.tls_dial_timeout` | `30` | `30` | TLS 拨号超时（秒） |
| `refresh.interval_minutes` | `30` | `30` | 后台刷新间隔（分钟） |
| `refresh.quota_recheck_minutes` | `30` | `30` | 已耗尽账号的重新检查间隔 |
| `refresh.concurrency` | `10` | `10` | 后台刷新并发数 |
| `refresh.live_check_seconds` | 未设置 | `5` | 同一账号请求级实时额度校验的最小间隔；`0` = 每次请求都查 |

## Register（批量 MS SSO 注册）

| 配置项 | 代码默认 | 说明 |
| --- | --- | --- |
| `register.history_file` | `accounts/.register_history.json` | Job 历史持久化文件，容器部署且使用 `ACCOUNTS_DIR` 时会自动映射到该持久卷 |
| `register.history_memory_cap` | `100` | 内存 / 磁盘最多保留多少个 Job |
| `register.default_concurrency` | `1` | Dashboard 注册抽屉的默认并发值 |

## 环境变量

`server.*` / `proxy.*` / `timeouts.*` / `refresh.*` / `browser.*` 都有对应大写下划线命名的环境变量。常用：

```bash
export PORT=3000
export API_KEY=sk-your-api-key
export ADMIN_PASSWORD=your-dashboard-password
# 可选：手动覆盖 Dashboard 显示的版本；Railway 仓库部署通常会自动提供提交号
export APP_VERSION=your-version
# 可选的 Dashboard 登录签名密钥；不填时自动使用 ADMIN_PASSWORD。
export DASHBOARD_SESSION_SECRET=your-stable-random-secret
export ENABLE_WEB_SEARCH=true
export ENABLE_WORKSPACE_SEARCH=false
export ASK_MODE_DEFAULT=false
export USE_CLIENT_SYSTEM_PROMPT=true
export USE_NOTION_PERSONAL_INSTRUCTIONS=false
export CHECK_PERSONAL_INSTRUCTIONS_ON_IMPORT=false
export ENABLE_TOOL_BRIDGE=true
export NOTION_PROXY=socks5h://127.0.0.1:1080
export QUOTA_LIVE_CHECK_SECONDS=5
export REFRESH_CONCURRENCY=10
export CONFIG_PATH=/app/accounts/.notion-manager-config.yaml # 可选；ACCOUNTS_DIR 已设置时会自动使用这个路径
```

`NOTION_PROXY` 写错（不支持的 scheme 等）时，启动会打印一次日志并清空，避免错误的 URL 渗透到每个 Notion 请求里。

## 端点

| 路径 | 说明 | 鉴权 |
| --- | --- | --- |
| `GET /health` | 健康检查 + 账号池摘要 | 无 |
| `POST /v1/messages` | Anthropic Messages API | API Key |
| `GET /dashboard/` | 管理面板 | Dashboard 登录 |
| `GET /proxy/start` | 创建账号代理会话 | Dashboard 登录 |
| `GET /ai` | Notion Web 代理入口 | `np_session` |
| `GET /admin/accounts` | 账号列表（支持 `q` / `page` / `page_size`） | Dashboard 会话 |
| `DELETE /admin/accounts/{email}` | 删除账号 JSON + 池中条目 | Dashboard 会话 |
| `GET /admin/models` | 模型映射 + 池内可用模型 | Dashboard 会话 |
| `GET/POST /admin/refresh` | 查询 / 触发刷新 | Dashboard 会话 |
| `GET/PUT /admin/settings` | 读 / 写运行时设置（`enable_web_search` / `enable_workspace_search` / `ask_mode_default` / `use_client_system_prompt` / `use_notion_personal_instructions` / `check_personal_instructions_on_import` / `enable_tool_bridge` / `debug_logging` / `notion_proxy`） | Dashboard 会话 |
| `GET /admin/stats` | Token 用量统计（累计 + 今日 + 24h + 30 天序列 + Top‑N） | Dashboard 会话 |
| `POST /admin/register` | 兼容历史的同步批量注册 | Dashboard 会话 |
| `GET /admin/register/providers` | Provider 列表 | Dashboard 会话 |
| `POST /admin/register/start` | 启动批量注册 Job | Dashboard 会话 |
| `GET /admin/register/jobs[?limit=]` | 最近 Job 列表 | Dashboard 会话 |
| `GET /admin/register/jobs/{id}` | 单 Job 快照 | Dashboard 会话 |
| `GET /admin/register/jobs/{id}/events` | SSE 事件流 | Dashboard 会话 |
| `POST /admin/register/jobs/{id}/retry` | 仅重试失败行 | Dashboard 会话 |
| `DELETE /admin/register/jobs/{id}` | 删除 Job + sidecar | Dashboard 会话 |

## 项目结构

```text
notion-manager/
├── cmd/notion-manager/        # 服务入口
├── cmd/register/              # 独立的批量注册 CLI
├── internal/proxy/            # 账号池、API handler、反向代理、上传、配置、统计
├── internal/msalogin/         # Microsoft 消费者 SSO + Notion onboarding 流程
├── internal/regjob/           # Provider 可插拔的批量注册 runner + store
├── internal/regjob/providers/ # Provider 实现（microsoft/、…）
├── internal/netutil/          # 代理拨号工具
├── internal/web/dist/         # 嵌入式 Dashboard 静态资源
├── web/                       # Dashboard 前端源码（React + Vite）
├── chrome-extension/          # 提取 Notion 会话的 Chrome 扩展
├── accounts/                  # 账号 JSON + .register_history.json + .token_stats.json
├── docs/                      # 中英文档
├── example.config.yaml
├── README.md
└── README_CN.md
```

## 使用建议与限制

- `admin_password` 留空会自动生成随机密码并打印到控制台，哈希后写回当前配置文件。**首次启动一定要保存控制台显示的明文密码**，事后无法恢复
- 反向代理优先使用包含 `full_cookie` 的账号；Microsoft SSO 注册器生成的账号自带可用的 `full_cookie`
- Free / Plus 的 complimentary trial 用完后可能长期不可用，但账号文件会保留并持续复查，升级到 Business / Enterprise 后可自动恢复
- 修改 `web/` 前端源码后需要在 `web/` 目录执行 `npm run build`，并把产物同步到 `internal/web/dist/`，否则运行时仍走旧资源
- `accounts/.register_inputs/<job_id>.json` 里有明文凭据，**不要**纳入版本控制；`.gitignore` 已排除该目录
