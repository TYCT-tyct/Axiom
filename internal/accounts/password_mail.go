package accounts

import (
	"html"
	"strings"

	"axiom/internal/platform/mail"
)

// passwordMail keeps untrusted brand text escaped in HTML and out of headers containing control characters.
func passwordMail(to, brand, language, link string) mail.Message {
	brand = strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, brand)
	subject, text := brand+" — password changed", "Your password has changed. If this was not you, contact the site operator immediately."
	if language == "zh" {
		subject, text = brand+" — 密码已变更", "您的密码已变更。如果不是您本人操作，请立即联系站点管理员。"
	}
	if link != "" {
		subject, text = brand+" — reset your password", "Reset your password using the link below. It expires 30 minutes after your request. If you did not request this, ignore this email."
		if language == "zh" {
			subject, text = brand+" — 重置密码", "请使用以下链接重置密码，链接在请求后 30 分钟过期。如果不是您本人请求，请忽略此邮件。"
		}
	}
	msg := mail.Message{To: to, Subject: subject, Text: brand + "\n\n" + text, HTML: "<h1>" + html.EscapeString(brand) + "</h1><p>" + html.EscapeString(text) + "</p>"}
	if link != "" {
		msg.Text += "\n\n" + link
		msg.HTML += "<p><a href=\"" + html.EscapeString(link) + "\">" + html.EscapeString(link) + "</a></p>"
	}
	return msg
}
