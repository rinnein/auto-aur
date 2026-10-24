# auto-aur

自动把 GitHub Release 发布到 AUR 的工具。用 Go 编写，由 GitHub Actions 定时驱动。

许可证仅适用于脚本本身，不适用于生成的 AUR 包。

## 工作方式

对 `conf.json` 里每个启用的任务：

1. 从 GitHub 读取目标 release（`latest` / 按 tag / 按关键字搜索）。
2. 渲染 `PKGBUILD`，生成 `.SRCINFO`（优先用 `makepkg --printsrcinfo`，没有 makepkg 时用内置生成器）。
3. 与已发布的 `auto-aur.lock.json` 和 `PKGBUILD` 对比；版本号和元数据都没变则跳过。
4. 把改动推到 AUR 的对应包仓库；**如果 AUR 上还没有这个包，会自动创建**。
5. 写出 `out/update.sh`，由 workflow 最后一步统一执行。

## 用法

```sh
go build -o auto-aur .
./auto-aur tasks            # 列出已注册的任务
./auto-aur run              # 跑 conf.json 里的全部启用任务
./auto-aur run -dry-run     # 只渲染并打印，不写文件、不推送
./auto-aur run -only animeko -force -v
```

`-force` 忽略 `auto-aur.lock.json`（等同于 workflow 里的 `force_update`）。`-no-create` 让缺失的 AUR 包直接失败而不是创建。

## conf.json

```json
{
  "maintainer": "Your Name <you@example.com>",
  "aur": { "host": "aur.archlinux.org", "user": "aur", "branch": "master" },
  "createOnMissing": true,
  "packages": [
    { "task": "animeko" },
    { "task": "animeko-beta", "overrides": { "depends": ["vlc-plugin-ffmpeg", "gvfs"] } },
    { "task": "animeko-alpha", "disabled": true }
  ],
  "disabled": []
}
```

- `packages` 决定要执行哪些任务，顺序即执行顺序。初始为空 —— 按需添加。
- 禁用任务有两种写法：条目上 `"disabled": true`，或把任务名（支持 `*` 通配，如 `animeko-*`）放进顶层 `"disabled"`。被禁用的任务完全不碰 AUR。
- `overrides` 可覆盖 `pkgname` / `depends` / `conflicts` / `provides` / `pkgdesc` / `url` / `license` / `arch` / `options` / `pkgrel` / `disname` 等字段。
- `aur` 段省略时使用上面的默认值，与 workflow 写入的 `~/.ssh/config` 一致。

## 环境变量

| 变量 | 用途 |
| --- | --- |
| `GH_TOKEN` / `GITHUB_TOKEN` | 可选。提高 GitHub API 限额；不设也能跑，但容易遇到 403 限流。 |
| `AUR_SSH_KEY`（通过 `secrets`） | workflow 把它写入 `~/.ssh/aur_key`，供 `Host aur.archlinux.org` 使用。 |

## 新增一个任务

任务的生成逻辑在 `repo/`，注册表在 `repo/gen.go`。加一个 Animeko 频道只需新建一个文件：

```go
package animeko

import "github.com/rinnein/auto-aur/repo"

func init() {
    repo.Register("my-channel", New(Channel{
        Task:    "my-channel",
        Pkgname: "animeko-appimage-nightly",
        Search:  "nightly",
        Disname: "Animeko Nightly",
        Depends: previewDeps,
        IconDir: "512x512",
    }))
}
```

然后在 `conf.json` 的 `packages` 里加上 `{ "task": "my-channel" }`。其他软件需要新模板时实现 `repo.Generator` 接口即可。

## 开发

```sh
gofmt -l .        # 应无输出
go vet ./...
go test ./...
```

测试不依赖网络：GitHub 与 AUR RPC 用 fixture/httptest，AUR 的建包与更新流程用本地 bare 仓库模拟。
