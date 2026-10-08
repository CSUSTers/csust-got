# Agent v3 Telegram 投递保护

本文描述 Agent v3 把模型输出投递到 Telegram 时的保护逻辑，覆盖流式（`handleStreaming` → `streamToTelegramWithDelivery`）与非流式（`handleNonStreaming` → `nonStreamResponseWithDelivery`）两条路径。实现位于 `agent/streaming.go` 与 `agent/format.go`。

## 1. 最终投递（A4）

### 1.1 Flood 重试

- 最终编辑或最终发送返回 `tb.FloodError` 时，等待 `RetryAfter`（上限 `telegramFloodRetryCap` = 60s，受 turn 的 `context` 控制，超时即放弃）后重试一次。
- 第二次仍为 flood 则按真实失败处理；flood 不会触发纯文本回退。
- "message is not modified" 视为成功（`telegramDeliveryProof`），不再重发。

### 1.2 格式回退

- 带 parse mode 的编辑/发送被 Telegram 拒绝（非 flood 错误）时，用**未格式化的原文**和 `tb.ModeDefault` 重试一次；不再使用已转义的 MarkdownV2 文本重试。
- 回退仍失败才返回错误；若 placeholder 是工具边界清空后的新内容（`deleteOnError`），保持原有的删除 placeholder 语义。

### 1.3 超长输出分段

- 以**格式化后**文本的 UTF-16 长度判断是否超过 `util.TelegramMessageLimit`（4096）。
- 超长时用 `formatTelegramChunks` 切分：对原文切分，再对每个原文片段单独格式化，确保 MarkdownV2/HTML 转义和 `quote`/`collapse`/`block` 包裹在每条消息内自洽（代码块围栏逐段闭合）。
- 切分点由共享切分器 `takeTelegramChunk` 决定：优先段落边界，其次行边界；跳过 ``` 围栏内部的边界；避免把一条消息切得不足一半（防止"一行标题 + 一大段"产生碎片消息）；都不满足时在 rune 边界硬切。
- 推理内容（reason）随第一段输出；若推理本身放不下，先按同样规则切成独立的引用段落。
- 第一段编辑 placeholder，后续各段依次作为上一段的回复发送（`AllowWithoutReply`）。所有已投递消息按顺序写入 `telegramResponseResult.deliveredAll`，`delivered` 为最后一条；`commitAgentV3Session` 当前仍只接收最后一条消息（见 §4）。
- 任一后续段失败即停止，并返回错误；已投递段保留。

### 1.4 Rich 消息回退

- `sendTelegramRichMessage` 失败时记录 `Warn` 日志，然后把 `delivery.VisibleText` 走普通（可分段）路径投递，不再直接删除 placeholder 并报错。
- 若回退也失败，返回的错误同时包含 rich 错误与回退错误（`%w; plain fallback: %w`）。

### 1.5 流式预览

- 生成过程中累计预览若超过 4096，仅显示尾部窗口并以 `…` 开头（`tailTelegramPreview`），避免 `MESSAGE_TOO_LONG` 导致中途编辑失败。

### 1.6 Cron 报告

- `cronReportChunks` 复用同一切分器，但保持原有的 1800 rune 分段尺寸，以免改变已有报告消息数量。

## 2. 流式编辑去重与退避（B1）

`streamProcessor.editPlaceholder` 维护以下状态：

- `lastSentFormatted`：上次成功发送的格式化文本。格式化结果未变化时跳过编辑；最终编辑若与之相同，直接把 placeholder 作为投递凭证，不再调用 API。
- `floodNotBefore`：收到 `tb.FloodError` 时记为 `now + RetryAfter`，在此之前的 tick 一律跳过。
- `floodBackoff`：flood 持续时有效编辑间隔翻倍（基数为配置的 `edit_interval`，至少 1s），上限 `streamingEditBackoffCap` = 10s；编辑成功后清零。
- 任何一次编辑尝试（无论成败）都会调用 `TurnContext.MarkEdited()`，避免门控每个 tick 都重新触发。
- 周期性编辑遇到非 flood 失败时，用原文 + `ModeDefault` 重试一次；遇到 flood 不重试、不等待，只记录退避。
- 最终编辑（`force`）走 §1.1 的等待重试逻辑。

## 3. 测试

- `agent/streaming_delivery_test.go`：`TestStreamFinalEditRetriesOnceAfterFlood`、`TestStreamFinalEditGivesUpAfterRepeatedFlood`、`TestStreamFinalLongOutputIsSplitIntoOrderedMessages`、`TestStreamFinalLongOutputStopsAtFirstFailedChunk`、`TestStreamFinalRichFailureFallsBackToPlainText`、`TestNonStreamRichFailureFallsBackToPlainText`、`TestNonStreamLongOutputIsSplitIntoOrderedMessages`、`TestNonStreamFinalSendRetriesOnceAfterFlood`、`TestNonStreamFormattedFailureRetriesWithRawText`、`TestUpdateMessageSkipsUnchangedText`、`TestUpdateMessagePausesEditsUntilFloodWindowPasses`、`TestUpdateMessageBackoffDoublesUpToCap`、`TestUpdateMessageRetriesRawTextOnceAndMarksEditTime`、`TestUpdateMessageShowsTailWhenPreviewExceedsLimit`。
- `agent/format_test.go`：`TestTakeTelegramChunkPrefersParagraphsAndAvoidsCodeFences`、`TestChunkPlainTelegramTextRoundTrips`、`TestFormatTelegramChunksFormatsEachChunkSeparately`、`TestFormatTelegramChunksKeepsReasonOnFirstChunk`、`TestTailTelegramPreview`。
- `util/utils_test.go`：`TestUTF16Len`、`TestFloodRetryAfterIgnoresOtherErrors`。

## 4. 待办

- `commitAgentV3Session(tc, sent *tb.Message)` 仍只接收最后一条消息。多段投递时 `DeliveryReceipt.MessageIDs` 应包含全部消息 ID；需要把签名改为接收 `[]*tb.Message`（或直接接收 `telegramResponseResult`），由 session 模块接入 `deliveredAll`。
