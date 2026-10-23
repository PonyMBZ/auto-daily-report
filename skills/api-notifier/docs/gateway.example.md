# 钉钉机器人网关 API（示例）

> 这是**脱敏的示例文档**，演示"把任意通知服务的 API 文档丢进来，AI 就能拼请求"。
> 用法：复制本文件为 `gateway.md`，把 URL / robotName / 手机号换成你自己的。

## 请求

| 项 | 值 |
|---|---|
| 方法 | POST |
| URL | http://your-robot-gateway.example.com/api/robot/send/markdown |
| Content-Type | application/json |

## 请求体字段

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| robotName | string | 是 | 机器人名, 固定填 `daily-report_bot` |
| title | string | 是 | 通知标题, 原样使用调用方给的 title |
| content | string | 是 | markdown 正文, 原样使用调用方给的 content |
| atMobiles | string[] | 否 | 要 @ 的手机号列表, 如 `["+86-13800000000"]`; 不需要 @ 就传 `[]`; 如果要 @ 人, content 最后也必须加上 @+86-13800000000 |
| atAll | bool | 否 | 是否 @ 所有人, 默认 `false` |

## 响应

- HTTP 状态码 200 = 消息已投递。
- 非 200 视为发送失败, 在 done 的结果里带上 status 说明。

## 示例

```json
{
    "robotName": "daily-report_bot",
    "title": "日报通知",
    "content": "# 今日日报\n\n完成了三件事\n @+86-13800000000",
    "atMobiles": ["+86-13800000000"],
    "atAll": false
}
```
