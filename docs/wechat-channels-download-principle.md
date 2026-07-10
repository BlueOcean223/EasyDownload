# 微信视频号检测与下载原理

本文记录微信视频号从 MITM 检测到 v2 下载任务、解密和最终发布的当前边界。

## 1. 检测与私有执行数据

MITM 只覆盖视频号页面和脚本白名单，不解密视频 CDN。代理回调把微信 `VideoInfo` 交给 detection adapter；adapter 使用微信稳定 ID/page key 或已知稳定 URL 参数生成 source-scoped identity，并为不同格式/画质建立 opaque candidate ID。

媒体 URL、headers、decode key 和原始 platform content ID 只保存在后端领域模型。HTTP/Wails 的 `VideoDTO` / `ResourceDTO` 只包含展示字段、加密提示和 opaque ID。每次 Store mutation 都返回带 revision 的完整权威快照，前端不实现微信去重或合并。

用户点击下载时只调用：

```text
StartDetectedDownload(detectionID, candidateID)
```

后端解析私有 candidate，将 URL、headers、decode key 和 file format 编码进微信 adapter 自有的 `PlatformData`，再通过统一 `CreateTask` 建立 v2 task。

## 2. Fetch、解密与 PublishFinal

微信 adapter 从 `TaskExecutionContext` 取得 composition root 注入的共享 Fetcher，把字节写到任务专属 encrypted temporary 文件和原子 resume sidecar。Fetch 负责 validator/`If-Range`、安全重下、no-progress timeout、短重试、size/SHA 和 progress reset；它不直接写最终输出。

若资源需要解密，adapter 先复制出独立 derived decrypt temporary，再执行解密和视频格式验证。解密失败会删除不可信 derived 文件，但保留已经验证的 encrypted temporary 供重试；下一次执行不能把上次崩溃留下的不完整 derived 文件当作输入。

通过格式、size 和 SHA 校验后，adapter 调用 manager 的 `PublishFinal`。Manager 先持久化 publish intent，再 no-replace 发布到 `OutputPathAllocator` 预留路径，最后原子记录 primary final artifact 与 `completed`。前端永远看不到 decode key、signed URL、headers、publish intent 或 reservation key。

## 3. 停止、清理与恢复

pause/cancel/remove 先返回 accepted receipt，终态通过带 revision 的 lifecycle event 发布。Coordinator 必须先等待 Fetch/解密 worker 退出：pause/shutdown 保留 encrypted partial 和 sidecar；cancel/remove 恰好 cleanup 一次任务专属 encrypted/derived temporary 与 sidecar。超时保持 `stopping` 并在后台继续收口。

新任务状态写入 `downloads.v2.json`，旧 `downloads.json` 不导入且保持原样。重启后可凭持久化的微信 `PlatformData` 恢复；adapter 在解码前要求当前 `PlatformDataVersion`，未知版本直接 fail-closed。若最终文件已经发布但 completion 快照尚未提交，则按 publish intent 的 size/SHA 认领，不能重复覆盖。
