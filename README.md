# Jar-Fucker

> 基于 Go 的 JAR 包提取与 Fernflower 反编译工具 —— 内置 Web 图形界面，单文件即可运行

## 功能特性

- **图形化界面** — 内嵌 Web UI，深色主题，VS Code 风格布局
- **一键反编译** — 选择 JAR 文件，自动提取 + Fernflower 反编译
- **自动识别 Fernflower** — 支持 `fernflower.jar`、源码 zip 或解压目录
- **文件浏览器** — 可视化浏览本地文件系统选择 JAR
- **目录树** — 反编译结果以树形结构展示
- **语法高亮** — 内置 Java 代码语法高亮查看器
- **多标签页** — 同时打开多个文件，标签切换
- **代码搜索** — 在反编译结果中搜索关键词，高亮匹配
- **JAR 分析** — 查看包结构、文件统计、MANIFEST 信息
- **任务日志** — 反编译或构建失败时显示 Fernflower/Gradle 错误详情
- **配置持久化** — Java 与 Fernflower 路径保存到 `.jar-fucker.json`
- **单文件部署** — 编译后为单个可执行文件，所有资源内嵌
- **键盘快捷键** — Ctrl+O 浏览 / Ctrl+F 搜索 / Ctrl+Enter 反编译

## 环境要求

- Go >= 1.22 (编译)
- Java JDK/JRE (运行 Fernflower；源码 zip 首次构建需要 JDK)

## 快速开始

```bash
# 克隆项目
git clone https://github.com/hg0434hongzh0/Jar-Fucker.git
cd Jar-Fucker

# 编译
go build -o jar-fucker .

# 运行 (自动打开浏览器)
./jar-fucker
```

启动后会自动在浏览器中打开图形界面 (默认端口 9527)。

## 使用方法

### 1. 选择 JAR 文件

- 直接在顶部输入框中输入 JAR 文件的绝对路径
- 或点击「浏览」按钮，通过文件浏览器选择

### 2. 反编译

- 点击「反编译」按钮或按 `Ctrl+Enter`
- 工具会使用 Fernflower 提取并反编译 JAR 包
- 未手动填写输出目录时，结果输出到带时间戳的 `<源目录>_decompiled/<时间戳>/` 目录

### 3. 浏览源码

- 左侧文件树展示反编译后的目录结构
- 点击 `.java` 文件在右侧查看，支持语法高亮

### 4. 搜索代码

- 点击搜索按钮或按 `Ctrl+F`
- 输入关键词搜索所有反编译的 Java 文件

### 5. 设置

- 点击齿轮图标可配置：Java 路径、Fernflower 路径、输出目录、过滤包名
- 保存后配置会写入当前工作目录的 `.jar-fucker.json`

## 自定义端口

```bash
PORT=8080 ./jar-fucker
```

## 项目结构

```
Jar-Fucker/
├── main.go                  # 入口，启动 HTTP 服务
├── go.mod
├── internal/
│   ├── handler/
│   │   └── handler.go       # HTTP API 处理器
│   ├── jar/
│   │   └── jar.go           # JAR 分析/提取/搜索
│   └── cfr/
│       └── cfr.go           # Fernflower 构建与反编译
├── web/
│   ├── index.html           # 前端页面
│   ├── style.css            # 样式 (Catppuccin 深色主题)
│   └── app.js               # 前端逻辑
└── README.md
```

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/analyze` | 分析 JAR 包信息 |
| POST | `/api/decompile` | 反编译 JAR 包 |
| GET | `/api/tree?root=` | 获取文件目录树 |
| GET | `/api/file?path=` | 读取文件内容 |
| POST | `/api/search` | 搜索代码关键词 |
| GET | `/api/browse?dir=` | 浏览本地文件系统 |
| GET | `/api/config` | 获取配置 |
| PUT | `/api/config` | 更新配置 |

## 许可证

MIT License
