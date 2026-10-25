# auto-aur

自动把 GitHub Release 发布到 AUR 的框架。用 Go 编写，由 GitHub Actions 定时驱动。

当前内置任务（`repo/your-szu-life/`），对应 [rinnein/your-szu-life](https://github.com/rinnein/your-szu-life)：

| 任务 | 包名 | 来源 |
| --- | --- | --- |
| `your-szu-life-bin` | `your-szu-life-bin` | `canary` release 里的预编译 `.deb` |
| `your-szu-life-git` | `your-szu-life-git` | `main` 分支源码构建 |

**版本与校验和都在 CI 运行时从 release 实时确定**，代码里没有任何写死的版本或 hash：

- `-bin` 的 `pkgver` 取自 release 声明的版本（release 名称 / body，回退到资产文件名）；`sha256sums` 直接读 release 资产的 `digest` 字段（GitHub 上传时记录），只有该字段缺失时才下载文件现算 —— 因此正常情况下不再下载 39 MB。
- `-git` 完全由指定的 **`main` 分支**驱动：源码是 `git+…/your-szu-life.git#branch=main`，版本读自该分支的 `package.json`，再附上分支 HEAD 的短 sha。PKGBUILD 里的 `pkgver()` 会在构建时从同一份 `package.json` 复现，保证 makepkg 算出的版本与发布值一致。

两个包的 `pkgdesc` 都取自上游仓库的 **About description**（GitHub 上的仓库简介），每次运行动态获取：改一次 GitHub 上的简介，AUR 上的描述就会跟着更新（并自动 `pkgrel+1` 让已安装用户收到）。仓库没填简介时退回内置默认值，并在运行摘要里说明。

另外：deb 里自带了一个 Debian 版的 `aria2c`，它依赖的 `libaria2.so.0` 并不在 deb 内，装到 `/usr/bin/aria2c` 会与 `aria2` 包冲突（`filesystem conflict on /usr/bin/aria2c`）。打包时该文件会被删除，改由 `depends=('aria2')` 提供真正的 `aria2c`。

两个包都 `provides=('your-szu-life')` 且互相 `conflicts`，因此：

```sh
paru -S your-szu-life     # 会列出上面两个包供选择
paru -S your-szu-life-bin # 直接装预编译版
paru -S your-szu-life-git # 直接装源码构建版
```

**刻意不发布真正的 `your-szu-life` 包**：一个真实包名会遮蔽 provider 列表，`paru -S your-szu-life` 就变成直接安装它，用户失去选择权。

许可证仅适用于脚本本身，不适用于生成的 AUR 包。

## 工作方式

对 `conf.json` 里每个启用的任务：

1. 从 GitHub 读取上游：release（`latest` / 按 tag / 按关键字搜索）或分支最新提交（`-git` 包）。
2. 渲染 `PKGBUILD` 并生成 `.SRCINFO`。生成器不依赖 makepkg；`TestSrcinfoMatchesMakepkg`
   会用系统上的 makepkg 校验两者逐字节一致。
3. 与已发布的 `auto-aur.lock.json` 和 `PKGBUILD` 对比；版本号和元数据都没变则跳过。
4. 把改动推到 AUR 的对应包仓库；**如果 AUR 上还没有这个包，会自动创建**。

推送由 Go 程序在本步骤内直接完成，workflow 里不再有第二个执行 shell 的步骤。

## 由上游的发布流程自动触发

定时任务最多晚一天；如果你希望**上游发布完 release 的那一刻**就打 AUR，让上游
workflow 反过来拉起本仓库即可。本仓库的 workflow 监听 `repository_dispatch`
（事件类型 `aur-release`），payload 决定这次运行看哪些包：

```json
{
  "event_type": "aur-release",
  "client_payload": {
    "source": "rinnein/your-szu-life",
    "tasks": ["your-szu-life-bin"],
    "tag": "canary",
    "force": false
  }
}
```

| 字段 | 含义 |
| --- | --- |
| `source` | 触发事件的上游仓库（`owner/name`，也接受完整 URL 或 `.git` 后缀）。该仓库**无任何任务跟踪**时整次运行是空操作。 |
| `tasks` | 要发布的任务名，支持 `your-szu-life-*` 前缀；省略表示该上游的全部任务。 |
| `tag` / `search` | 事件对应的 release。发布型任务只在自己确实打包这个 tag 时才会运行；`-git` 任务不看它，由 lock 文件决定是否有变化。 |
| `force` | 忽略 `auto-aur.lock.json` 强制重发（等同于 `force_update`）。 |

在别处触发：

```sh
# 用 curl 直接派发
curl -sSf -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/rinnein/auto-aur/dispatches \
  -d '{"event_type":"aur-release","client_payload":{"source":"rinnein/your-szu-life","tasks":["your-szu-life-bin"],"tag":"canary"}}'

# 或者用本仓库自带的客户端（自动读 GH_TOKEN，-dry-run 只打印）
go run ./cmd/gh dispatch -repo rinnein/auto-aur \
  -source rinnein/your-szu-life -tasks your-szu-life-bin -tag canary
```

`examples/upstream-aur-release.yml` 是一份可直接放进上游仓库的完整示例：
`release: [published, edited]` 后派发，只发布该 tag 对应的子包。上游只需要加一个
对 `rinnein/auto-aur` 有 `Actions: write` 的 token 作为 secret。

`-git` 包每次都会看到新的 HEAD，所以从上游派发时通常只传 `your-szu-life-bin`；
`-git` 交给每天的定时任务或另一次派发更省时间。

**为什么 payload 不能凭空加包**：派发方只能从 `conf.json` 已启用的任务里选，
不能引入新任务、新上游或新参数。`repo.RequireUpstream` 会复核任务跟踪的仓库与
`source` 是否一致，所以一个针对 `attacker/fork` 的派发找不到任何可发布的包，
只会安静地什么都不做。这个仓库的 `workflow_dispatch` 与定时任务不受影响，
行为与以前完全一致。

想先看看某次派发会选中什么，不碰 AUR：

```sh
auto-aur trigger -source rinnein/your-szu-life -tag canary   # 列出会被选中的任务
auto-aur run -from-event -dry-run < event.json               # 只渲染，不写不推
```

`-from-event` 在有 `GITHUB_EVENT_PATH` 时读该文件，否则读 stdin。

## 用法

```sh
go build -o auto-aur .
./auto-aur tasks            # 列出已注册的任务（当前为空）
./auto-aur run              # 跑 conf.json 里的全部启用任务
./auto-aur run -dry-run     # 只渲染并打印，不写文件、不推送
./auto-aur run -only <task> -force -v
```

`-force` 忽略 `auto-aur.lock.json`（等同于 workflow 里的 `force_update`）。`-no-create` 让缺失的 AUR 包直接失败而不是创建。

## conf.json

```json
{
  "maintainer": "Your Name <you@example.com>",
  "aur": { "host": "aur.archlinux.org", "user": "aur", "branch": "master" },
  "createOnMissing": true,
  "packages": [
    { "task": "your-szu-life-bin" },
    { "task": "your-szu-life-git" }
  ],
  "disabled": []
}
```

- `packages` 决定要执行哪些任务，顺序即执行顺序。加 `{ "task": "..." }` 启用，删掉即停用。
- 禁用任务有两种写法：条目上 `"disabled": true`，或把任务名（支持 `*` 通配，如 `app-*`）放进顶层 `"disabled"`。被禁用的任务完全不碰 AUR —— 不查询、不克隆、不渲染。
- `overrides` 可覆盖 `pkgname` / `depends` / `conflicts` / `provides` / `pkgdesc` / `url` / `license` / `arch` / `options` / `pkgrel` / `disname` 等字段。
- `aur` 段省略时使用上面的默认值，与 workflow 写入的 `~/.ssh/config` 一致。

## 环境变量

| 变量 | 用途 |
| --- | --- |
| `GH_TOKEN` / `GITHUB_TOKEN` | 可选。提高 GitHub API 限额；不设也能跑，但容易遇到 403 限流。workflow 在未配置 `secrets.GH_TOKEN` 时回退到内置的 `github.token`。 |
| `AUR_SSH_KEY`（通过 `secrets`） | workflow 把它写入 `~/.ssh/aur_key`，供 `Host aur.archlinux.org` 使用。 |
| `GITHUB_EVENT_PATH` | 由 Actions 自动设置。`run -from-event` / `trigger -from-event` 优先读这个文件里的 dispatch payload。 |
| `GH_REPOSITORY` | `cmd/gh` 的目标仓库默认值（`owner/name`），Actions 里会自动注入。 |

## 新增一个任务

实现 `repo.Generator` 接口，并在 `init()` 里注册。新建目录 `repo/<你的包>/`：

```go
package mypkg

import (
    "github.com/rinnein/auto-aur/internal/pkgbuild"
    "github.com/rinnein/auto-aur/repo"
)

func init() { repo.Register("my-task", &Generator{}) }

type Generator struct{}

func (g *Generator) Pkgname() string { return "my-package-bin" }

func (g *Generator) Wanted() repo.Wanted {
    return repo.Wanted{Repo: "owner/project"} // Search/Tag 可选
}

func (g *Generator) Render(rel repo.Release, sum repo.Checksum) (repo.Generated, error) {
    digest, err := sum("https://example.com/SHA1SUMS") // 取校验和内容
    if err != nil {
        return repo.Generated{}, err
    }
    src, err := pkgbuild.Render(pkgbuild.Data{
        Maintainer: "Your Name <you@example.com>",
        Pkgname:    "my-package-bin",
        PkgnameIn:  "mypkg",
        Appimage:   "mypkg-" + rel.TagName + "-linux-x86_64.appimage",
        Pkgver:     rel.TagName,
        Pkgrel:     1,
        Pkgdesc:    "example",
        URL:        "https://example.com",
        License:    []string{"MIT"},
        Arch:       []string{"x86_64"},
        Depends:    []string{"glibc"},
        Provides:   []string{"mypkg"},
        Options:    []string{"!strip"},
        SourceURL:  "https://example.com/mypkg.appimage",
        SHA1:       digest,
        IconDir:    "512x512",
    })
    if err != nil {
        return repo.Generated{}, err
    }
    return repo.Generated{
        Pkgname:  "my-package-bin",
        Pkgver:   rel.TagName,
        Pkgrel:   1,
        PKGBUILD: src,
        Fields:   pkgbuild.Parse(src), // 供“元数据变更也需重发”检测
        Source:   "mypkg::https://example.com/mypkg.appimage",
        SHA1:     digest,
    }, nil
}

func (g *Generator) Srcinfo(gen repo.Generated) string { /* 组装 .SRCINFO */ }
```

然后在 `main.go` 里加一行 blank import：

```go
_ "github.com/rinnein/auto-aur/repo/mypkg"
```

最后在 `conf.json` 的 `packages` 里加上 `{ "task": "my-task" }`。

若想让任务支持 `conf.json` 的 `overrides`，额外实现：

```go
func (g *Generator) WithMaintainer(m string, o conf.Overrides) repo.Generator
```

`pkgbuild.Render` 内置的模板面向 AppImage 形态的包（`prepare/build/package` 三段、desktop 与图标安装）；其他形态的包直接自己拼 PKGBUILD 文本即可，`repo.Generated` 只要求最终的字符串和解析后的字段。

## 目录结构

```
main.go              CLI 入口（run / trigger / tasks）
cmd/gh/              从别的 workflow 派发本仓库的客户端
conf.json            任务配置
repo/               任务注册表、Generator 接口、trigger 选择逻辑（不含具体包）
internal/conf/      配置加载、校验、禁用判定
internal/github/    release / 分支 / 仓库信息读取，以及 dispatch 派发
internal/pkgbuild/  PKGBUILD 渲染与字段解析
internal/aur/       RPC 查询 + 包仓库克隆/创建/推送
internal/gitx/      git 调用封装
internal/fsx/       文件与 lock 读写
internal/app/       编排与发布
```

## 开发

```sh
gofmt -l .        # 应无输出
go vet ./...
go test ./...
```

测试不依赖网络：GitHub 与 AUR RPC 用 fixture/httptest，AUR 的建包与更新流程用本地 bare 仓库模拟。

## 变化检测与版本

每个任务在 `auto-aur.lock.json` 里记录 `pkgver`、`pkgrel` 和 `fingerprint`：

- **fingerprint** 标识上游的确切内容。release 任务用 tag + 发布时间 + 每个资产的名字/大小/更新时间；`-git` 任务用提交 SHA。
- 版本变了 → 重发，`pkgrel` 归 1。
- 版本没变但 fingerprint 变了（比如 `canary` 用同一个版本号重建了 deb）→ **`pkgrel` 自增**，否则 pacman 会认为已是最新而不升级。
- 两者都没变 → 再比较 PKGBUILD 的字段（依赖、描述等）；字段变了同样 `pkgrel+1` 后重发，字段也没变才跳过。

`-bin` 包每次运行都会下载一次 deb 来算 sha256（release 没有附带校验和文件），约 39 MB；`-git` 包不下载任何东西。
