# auto-aur

自动把 GitHub Release 发布到 AUR 的框架。用 Go 编写，由 GitHub Actions 定时驱动。

当前内置任务（`repo/szulife/`），对应 [rinnein/your-szu-life](https://github.com/rinnein/your-szu-life)：

| 任务 | 包名 | 来源 |
| --- | --- | --- |
| `your-szu-life-bin` | `your-szu-life-bin` | `canary` release 里的预编译 `.deb` |
| `your-szu-life-git` | `your-szu-life-git` | `main` 分支源码构建 |

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
2. 渲染 `PKGBUILD`，生成 `.SRCINFO`（优先用 `makepkg --printsrcinfo`，没有 makepkg 时用内置生成器）。
3. 与已发布的 `auto-aur.lock.json` 和 `PKGBUILD` 对比；版本号和元数据都没变则跳过。
4. 把改动推到 AUR 的对应包仓库；**如果 AUR 上还没有这个包，会自动创建**。
5. 写出 `out/update.sh`，由 workflow 最后一步统一执行。

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
| `GH_TOKEN` / `GITHUB_TOKEN` | 可选。提高 GitHub API 限额；不设也能跑，但容易遇到 403 限流。 |
| `AUR_SSH_KEY`（通过 `secrets`） | workflow 把它写入 `~/.ssh/aur_key`，供 `Host aur.archlinux.org` 使用。 |

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
main.go              CLI 入口
conf.json            任务配置
repo/               任务注册表与 Generator 接口（不含具体包）
internal/conf/      配置加载、校验、禁用判定
internal/github/    release 读取
internal/pkgbuild/  PKGBUILD 渲染与字段解析
internal/aur/       RPC 查询 + 包仓库克隆/创建/推送
internal/gitx/      git 调用封装
internal/fsx/       文件与 lock 读写
internal/app/       编排、发布、update.sh 生成
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
- 两者都没变 → 只比较 PKGBUILD 的字段，字段也没变才跳过。

`-bin` 包每次运行都会下载一次 deb 来算 sha256（release 没有附带校验和文件），约 39 MB；`-git` 包不下载任何东西。
