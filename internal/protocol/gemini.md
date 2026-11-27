# protocol 的 Gemini


Gemini 不是路由用的格式，在边缘与 Chat 互换，两个方向读同一组结构和表（`gemini.go`）：客户端用 `GeminiToChat` 和 `GeminiReply`（流按事件、整份按 `Body`、错误按 `GeminiErrorBody`），上游用 `ChatToGemini` 和 `ChatFromGemini`（把 `streamGenerateContent?alt=sse` 读成 Chat 流）。没有 ID 的调用按位置编号，结果按 ID 否则按同名最早未答的配对；思考签名在 Chat 一侧放 `extra_content.google.thought_signature`，转往 Gemini 时缺签名的首个调用写官方的跳过值；`thoughtsTokenCount` 计入输出和推理，各模态明细（音频、图片、视频，含缓存的）对应 Chat 明细的同名成员；中途的用量只把输入侧随下一个 delta 交中继，结束回答的那帧才报输出；出过内容后失败：整份回 502 带用量，流先发末段再报错。表达不了又改变回答的：入站点名拒绝，出站换端点。

