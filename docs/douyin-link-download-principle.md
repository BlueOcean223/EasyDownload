# 抖音链接解析下载原理

本文档说明 EasyDownload 中抖音“根据分享链接解析并下载”的完整链路。抖音侧整体遵循 `Parser -> Client -> Downloader -> App` 的结构：先把分享文本统一收敛为 `aweme_id`，再由 `Client` 优先解析分享页 SSR 数据，失败后回退到详情接口和 `slidesinfo`，最后交给下载器落盘。普通视频在返回数据缺少清晰度列表时，还会通过 `play_addr.uri` 做 ratio 阶梯探测，识别真实可下载档位。

## 1. 适用范围

当前这套链路处理的是“用户已经拿到抖音分享文本或链接”的场景，不处理搜索、推荐流发现、登录态管理或验证码绕过。

支持的典型输入包括：

- 抖音分享文本，文本里混有一条 `https://...` 链接
- 抖音短链接，例如 `https://v.douyin.com/xxxx/`
- 抖音完整视频链接，例如 `https://www.douyin.com/video/{aweme_id}`
- 抖音笔记链接，例如 `https://www.douyin.com/note/{aweme_id}`
- 图集分享链接，例如 `https://www.iesdouyin.com/share/slides/{aweme_id}/`
- 带 `modal_id` 或 `aweme_id` 查询参数的页面链接

核心代码：

- `internal/download/douyin/parser.go`
- `internal/download/douyin/client.go`
- `internal/download/douyin/downloader.go`
- `app.go`
- `frontend/src/views/Douyin.vue`

## 2. 总体流程

一次完整的抖音下载链路可以拆成下面几步：

1. 前端接收用户输入的分享文本或链接
2. `Parser` 从输入里找出第一个合法的抖音 URL
3. 如果 URL 是 `v.douyin.com` 短链，则先展开成真实长链接
4. 从长链接的路径或查询参数里提取 `aweme_id`
5. `Client` 用 `aweme_id` 优先请求分享页 SSR，失败再请求详情接口或 `slidesinfo`，构建 `DouyinItem`
6. 如果普通视频缺少 `bit_rate` 清晰度列表，基于 `play_addr.uri` 做 ratio 阶梯探测
7. 前端展示封面、作者、标题、清晰度、图集预览等信息
8. 用户点击下载后，`App` 层重新用原始分享文本走一遍解析和抓取
9. `Downloader` 按内容类型把资源落盘成 `.mp4` 或 `.zip`

这里有一个很重要的设计点：抖音下载入口依赖的是原始输入，不是前端缓存的 `DouyinItem`。这意味着下载时会重新解析和抓取一次，目的是保证后端始终以自己的最新结果为准。

## 3. 解析阶段

### 3.1 从分享文本中抽 URL

解析器先处理用户输入字符串：

- 去掉首尾空白
- 如果整段文本本身看起来就是 URL，优先直接解析
- 否则使用正则从分享文本里提取第一个 `http/https` URL
- 只接受抖音域名或已知短链域名

这一步解决的是“用户复制的不是纯链接，而是一整段分享口令文本”的问题。

### 3.2 什么是“3xx 短链展开”

抖音分享常见短链是 `v.douyin.com`。这类短链本身不是最终内容页，而是一个会返回 `301/302/303/307/308` 之类重定向响应的中转地址。

解析器的做法是：

1. 发送 `GET` 请求到短链
2. 禁止 HTTP 客户端自动跟随跳转
3. 手动读取响应头里的 `Location`
4. 把 `Location` 里的真实长链接作为下一跳继续解析

这么做的原因是，解析器并不关心中间过程，只想稳定拿到最终长链接里的 `aweme_id`。

### 3.3 支持哪些 ID 提取方式

短链被展开后，解析器会统一从 URL 里提取 `aweme_id`。提取来源包括：

- `/video/{id}`
- `/note/{id}`
- `/share/slides/{id}`
- 查询参数中的 `modal_id`
- 查询参数中的 `aweme_id`

如果 URL 已经不是抖音域名，或者路径里拿不到任何内容 ID，就返回解析失败。

### 3.4 解析阶段的边界

这一层只负责“把输入变成 `aweme_id`”，不会做：

- 内容详情抓取
- 视频清晰度选择
- 文件下载
- 登录态处理

也就是说，`Parser` 的职责非常单一：尽量把脏输入收敛成干净的内容 ID。

## 4. 信息抓取阶段

拿到 `aweme_id` 后，`Client` 当前采用“分享页优先、接口兜底”的顺序。每条路径返回后都会先做可用性检查：视频必须有可用 `streams`，图集必须有 `images`。如果某条路径能解析出对象但没有可下载媒体，会标记为“不完整”并继续尝试下一条路径。

客户端构造时会确保 `http.Client` 带有 `CookieJar`。优先请求分享页的原因是分享页本来就是官方 WebView/分享场景，带浏览器 `User-Agent` 和 `Referer` 就能拿到 SSR 数据，并且响应会自然下发匿名 `ttwid`，由 `CookieJar` 自动管理；实现不会主动调用额外的 `ttwid` 注册端点，也不依赖登录 Cookie。

### 4.1 第一条路径：分享页 SSR

客户端会先尝试分享页：

- `/share/video/{aweme_id}/?app=aweme`
- `/share/note/{aweme_id}/?app=aweme`

请求头同样模拟浏览器环境，然后从 HTML 中提取：

- `window._ROUTER_DATA`

解析时会遍历 `loaderData`，找到其中的 `videoInfoRes.item_list[0]`，再继续映射成内部对象。

这个路径现在是主路径，原因是：

- 分享页 SSR 对匿名访问更贴近官方分享场景
- 首次访问即可让服务端下发匿名 `ttwid`
- 普通视频通常能拿到 `play_addr.uri`，后续可用于清晰度探测

如果分享页被限流、返回空壳、页面结构变化，或者解析出的对象没有可用媒体，客户端不会立即失败，而是继续尝试接口兜底。

### 4.2 第二条路径：`aweme/detail`

分享页不可用时，客户端回退到官方 Web 详情接口：

- `https://www.iesdouyin.com/aweme/v1/web/aweme/detail/`

请求里会补一组固定查询参数，例如：

- `aweme_id`
- `aid=6383`
- `version_name=23.5.0`
- `device_platform=webapp`
- `os_version=2333`

请求头会模拟浏览器环境：

- `User-Agent`
- `Accept`
- `Accept-Language`
- `Origin: https://www.douyin.com`
- `Referer: https://www.douyin.com/`

客户端会先尝试解析新格式：

- `aweme_detail`

如果不行，再回退到旧格式：

- `item_list`

这一步不再是首选路径，但仍可在分享页空壳、HTML 结构变化或接口数据更完整时兜底。

### 4.3 第三条路径：`slidesinfo`

如果分享页和详情接口都不可用，客户端最后会尝试：

- `https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/`

这条路径主要是为了补图集或“看起来像图集、但每页里可能带视频”的内容。某些滑动作品在普通详情或分享页里只能看到静态封面，但 `slidesinfo` 能给出 `images[].video.play_addr` 这类更完整的媒体信息。

因此它不是通用替代，而是专门为图集/混合内容兜底。

### 4.4 视频流构建与 ratio 探测

视频流优先来自返回数据里的 `video.bit_rate`：每个条目对应一个清晰度档位，客户端会从 `gear_name` 或宽高推导 `qualityKey`，并优先选择无水印地址。

如果 `bit_rate` 为空，或者最终没有构造出可用 `streams`，客户端会检查 `video.play_addr.uri`。只要有 URI，就按固定阶梯探测 play 端点：

- `1080p`
- `720p`
- `540p`
- `480p`
- `360p`

探测 URL 形如：

- `https://aweme.snssdk.com/aweme/v1/play/?video_id={uri}&ratio={ratio}&line=0`

每次探测都发送 `GET` 请求，并带上 `Range: bytes=0-1`。只有满足下面条件的结果才会被认为是真实可下载档位：

- HTTP 状态是 `206 Partial Content`
- `Content-Range` 能解析出大于 0 的总大小
- `Content-Type` 看起来是视频或二进制流
- 文件总大小没有和已接受档位重复

这样可以避免“盲构造 URL 后实际回落到同一档位或不存在档位”的问题。探测成功后，客户端会用探测到的真实档位替换普通 fallback 流，并按分辨率排序。

### 4.5 元数据补全

如果拿到的是视频内容，客户端还会对缺少文件大小的清晰度流额外发送小范围 `GET` 请求：

- `Range: bytes=0-1`

优先从 `Content-Range` 解析总大小；如果服务端直接返回 `200 OK`，则回退使用 `Content-Length`。这里不再使用 `HEAD`，避免某些 CDN 节点对 `HEAD` 不稳定。

这样前端在展示清晰度选项时，就能带上更完整的尺寸和大小信息，而不是只显示分辨率。

## 5. 内部数据模型

抓取完成后，客户端会把抖音返回的数据统一映射成 `DouyinItem`。这个对象至少会包含：

- 内容 ID
- 标题
- 作者名
- 作者 ID
- 封面
- 内容类型
- 视频流列表
- 图集图片列表

### 5.1 视频内容

如果作品被识别成视频，`DouyinItem` 会包含：

- `Type = video`
- 多个清晰度流（来自 `bit_rate`，或在 `bit_rate` 缺失时来自 ratio 探测）
- 每个流对应的 `qualityKey`，例如 `1080p`、`720p`、`source`
- 宽高、码率、URL、文件大小

下载器后续就按 `qualityKey` 选流。探测出来的流可能没有码率，但会尽量带上文件大小。

### 5.2 图集内容

如果作品被识别成图集，`DouyinItem` 会包含：

- `Type = album`
- 图片列表
- 每个条目对应的图片 URL
- 如果图集项里嵌有视频，还会带出对应的视频 URL

这也是抖音图集和普通图片集合不一样的地方：它不一定全是静态图片。

## 6. 下载阶段

下载器根据 `DouyinItem` 的内容类型选择不同路径。

### 6.1 单视频下载

视频下载的处理顺序是：

1. 根据用户选择的 `qualityKey` 选流
2. 如果没选中，就回退到第一个可用流
3. 创建目标文件路径
4. 把 URL、请求头、断点续传策略和重试策略交给通用 Fetch 模块
5. Fetch 使用任务专属临时文件下载，支持 validator/sidecar 约束的 Range 续传、服务端忽略 Range 时安全重下、no-progress timeout、短重试和 progress reset
6. Adapter 校验临时文件后调用 `PublishFinal`，由 manager 按预留路径 no-replace 发布

这样做的目标是把传输能力集中在一个模块里：

- 不支持 Range 的源站也能正常下载
- 抖音私有下载循环不再重复实现文件写入、重试和续传
- 传输错误保留结构化类型，平台层只负责转换成抖音语义
- `429` 默认不由 Fetch 自动重试；只有平台明确允许时才加入 retryable status

### 6.2 图集下载

图集下载走统一的媒体下载核心逻辑：

1. 根据用户选择确定下载哪些索引
2. 在临时目录中为这次图集下载建立状态文件
3. 校验已经完成的临时文件是否还存在
4. 并发下载未完成的媒体项；图片和内嵌视频都通过 Fetch 写入临时文件
5. 周期性保存状态，支持中断后恢复
6. 全部完成后把临时文件打成 ZIP
7. 清理临时目录

抖音图集默认是并发下载，这和小红书图文的顺序下载不同。

### 6.3 局部下载

抖音还支持“只下载图集中的部分项”。下载器会：

- 去重用户传入的索引
- 校验索引边界
- 只为这些索引创建下载任务
- 最终仍输出 ZIP

这对图集很重要，因为用户未必需要整套图片/视频。

## 7. App 和前端的调用方式

### 7.1 前端页面

抖音页面的交互很简单：

1. 用户在 `frontend/src/views/Douyin.vue` 输入分享文本或链接
2. 点击“解析”后调用 `GetDouyinVideoInfo`
3. 前端根据返回的 `DouyinItem` 展示封面、作者、标题、图集或清晰度
4. 点击“下载视频”或“下载全部”后调用下载接口

### 7.2 App 层接口

后端暴露的主要方法有：

- `GetDouyinVideoInfo`
- `DownloadDouyinVideo`
- `DownloadDouyinAlbumPartial`

其中最值得注意的是：

- `GetDouyinVideoInfo`：只负责解析和抓详情
- `DownloadDouyinVideo`：内部会重新调用 `GetDouyinVideoInfo`
- `DownloadDouyinAlbumPartial`：也会重新走一次解析和抓详情

这意味着：

- 前端显示结果只是预览
- 真正创建下载任务时，后端会再确认一遍数据

### 7.3 下载管理器集成

App 层不会自己直接落盘，也不会创建不可持久化的下载闭包。创建任务时会把 `DouyinItem`、清晰度和局部图集索引序列化为 `platformData`，交给通用 `DownloadManager` 持久化。

这样做的好处是：

- 下载进度、完成、错误事件都能统一发给前端
- 图集的项目完成百分比和当前文件字节数是独立进度维度；字节回调不携带百分比时，管理器保留上一条项目百分比，不会在单张图片下载过程中反复归零
- 抖音、小红书、B 站、视频号可以共用下载队列机制
- 并发已满时任务保持 `pending` 等待调度，而不是创建后启动失败
- 文件命名、任务状态、取消/恢复等基础设施不需要重复实现
- App 重启后，未完成任务通过 `PlatformID=douyin` 找回适配器，并用持久化的 `platformData` 恢复执行
- 新任务写入 `downloads.v2.json`，旧 `downloads.json` 原样保留且不自动导入
- pause/shutdown 保留图集状态和 partial；cancel/remove 在 worker join 后清理；Wails 先返回 accepted receipt，再由 lifecycle event 发布终态
- 最终 ZIP/MP4 路径由 `OutputPathAllocator` 在创建时预留，同名任务使用 `auto_rename`，外部同名文件不会被覆盖

## 8. 常见失败点

抖音链路常见失败点主要有下面几类。

### 8.1 解析失败

常见原因：

- 分享文本里根本没有抖音 URL
- URL 不是抖音域名
- 短链没有返回合法重定向
- 最终长链接里拿不到 `aweme_id`

### 8.2 抓取失败

常见原因：

- 分享页返回 403/429、空壳 HTML，或 `window._ROUTER_DATA` 不再匹配
- 详情接口返回 403/429，或者没有 `aweme_detail` / `item_list`
- `slidesinfo` 不包含目标内容
- 普通视频既没有可用 `bit_rate`，也无法通过 `play_addr.uri` 探测出真实档位

### 8.3 下载失败

常见原因：

- 清晰度流 URL 失效
- Range/`Content-Range` 或资源身份不一致且无法安全完整重下
- 图集里的某一项媒体地址为空
- 临时目录或目标目录写入失败

## 9. 这套实现的特点

抖音当前实现的工程特征可以概括为：

- 输入非常宽松，尽量兼容分享文本
- 抓取阶段优先走分享页 SSR，再用 `aweme/detail` 和 `slidesinfo` 兜底
- 使用 `CookieJar` 自动管理分享页下发的匿名 `ttwid`，不依赖登录 Cookie
- 普通视频在 `bit_rate` 缺失时通过 ratio 探测识别真实清晰度，并用 Range GET 补文件大小
- 图集兼容性考虑得比较多，尤其是混合图/视频内容
- 下载阶段追求吞吐和恢复能力
- 下载入口设计成“重新解析”，而不是盲信前端结果
- v2 恢复在解码抖音 `PlatformData` 前校验版本，未知版本 fail-closed

如果后续要扩展抖音搜索、登录态或风控相关能力，最合理的方向不是在这个文档描述的链路上硬塞，而是单独新增“搜索 / 浏览器态抓取”子系统，再和当前下载链路汇合。
