# streamcheck


`streamcheck.Messages`、`Responses`、`Chat` 按官方文档检查结构（单事件 32 MiB），违反为 `ErrInvalid`。Messages：`message_start` 一次在最前，块 `index` 连续、start 到 stop 之间只有匹配的增量，块都关闭后才有 `message_delta`。Responses：`sequence_number` 递增（不要求连续），项和部件序号连续、所指的正打开，结尾时 `output` 条数等于加入的项数。Chat：`id` 不变，`finish_reason` 后无增量，工具调用续写或恰好开始下一个，用量块在选项都结束后，`[DONE]` 是唯一正常结尾，顶层 `error` 块结束流。结尾后还有事件、结尾前结束都不合规；未列出的事件类型照常接受，厂商会不通知地加类型。

