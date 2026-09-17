// 文本回退：渲染失败或未启用渲染时，把同一份卡片视图模型排成 MarkdownV2 文本。
// 直接吃卡片模型是为了让两条路径的条数、顺序与文案完全一致——
// 不会出现「图里 6 件、文本里 8 件」或者两处对同一个字段说两种话。
package codex

import (
	"fmt"
	"strings"

	"hd2_bot/src/core/bot"
	"hd2_bot/src/plugins/plugutil"
)

// FormatEquipmentText 把装备卡片排成文本。
// 与卡片同样只写命中的那一件（详情的顺序也一致），没命中时只出提示。
func FormatEquipmentText(card EquipmentCard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", bot.Escape(card.Meta.Title))
	if card.Intro != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.Intro))
	}
	if card.Empty != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.Empty))
	} else if detail := card.Detail; detail != nil {
		detailText(&b, detail)
	}
	for _, note := range card.Notes {
		fmt.Fprintf(&b, "%s\n", bot.Escape(note))
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}

// detailText 把单件详情排成文本：与卡片同一份模型、同一个顺序，不会出现两种说法。
func detailText(b *strings.Builder, detail *EquipmentDetail) {
	fmt.Fprintf(b, "\n*%s*", bot.Escape(detail.Name))
	if detail.Model != "" {
		fmt.Fprintf(b, "（%s）", bot.Escape(detail.Model))
	}
	b.WriteString("\n")
	if detail.English != "" {
		fmt.Fprintf(b, "%s\n", bot.Escape(detail.English))
	}
	if len(detail.Tags) > 0 {
		fmt.Fprintf(b, "%s\n", bot.Escape(strings.Join(detail.Tags, " ｜ ")))
	}
	if detail.Acquire != "" {
		fmt.Fprintf(b, "获取：%s\n", bot.Escape(detail.Acquire))
	}
	if len(detail.Arrows) > 0 {
		fmt.Fprintf(b, "召唤指令 %s\n", bot.Escape(strings.Join(arrowGlyphs(detail.Arrows), " ")))
	}
	if wiki := detail.Wiki; wiki != nil {
		if wiki.Unlock != "" {
			fmt.Fprintf(b, "解锁：%s\n", bot.Escape(wiki.Unlock))
		}
		if wiki.Desc != "" {
			fmt.Fprintf(b, "%s\n", bot.Escape(wiki.Desc))
		}
		if len(wiki.Traits) > 0 {
			fmt.Fprintf(b, "特性：%s\n", bot.Escape(strings.Join(wiki.Traits, " ｜ ")))
		}
	}
	for _, section := range detail.Sections {
		fmt.Fprintf(b, "%s\n", bot.Escape(section.Title))
		if cells := fieldsText(section.Cells); cells != "" {
			fmt.Fprintf(b, "  %s\n", bot.Escape(cells))
		}
	}
	for _, attack := range detail.Attacks {
		title := attack.Title
		if attack.Tag != "" {
			title += "（" + attack.Tag + "）"
		}
		fmt.Fprintf(b, "%s\n", bot.Escape(title))
		if cells := fieldsText(attack.Cells); cells != "" {
			fmt.Fprintf(b, "  %s\n", bot.Escape(cells))
		}
	}
	// 内置详情的数值比目录里那几项细（弹体、穿透分角度、特殊效果），文本回退也一样列出来。
	if wiki := detail.Wiki; wiki != nil {
		if cells := fieldsText(wiki.Cells); cells != "" {
			fmt.Fprintf(b, "详细属性 %s\n", bot.Escape(cells))
		}
		for _, attack := range wiki.Attacks {
			title := attack.Title
			if attack.Tag != "" {
				title += "（" + attack.Tag + "）"
			}
			fmt.Fprintf(b, "%s\n", bot.Escape(title))
			if cells := fieldsText(attack.Cells); cells != "" {
				fmt.Fprintf(b, "  %s\n", bot.Escape(cells))
			}
		}
		if len(wiki.Tips) > 0 {
			fmt.Fprintf(b, "使用策略：%s\n", bot.Escape(strings.Join(wiki.Tips, "；")))
		}
		if len(wiki.Variants) > 0 {
			fmt.Fprintf(b, "变体：%s\n", bot.Escape(strings.Join(wiki.Variants, "、")))
		}
	}
}

// FormatWarbondsText 把军需簿卡片排成文本：明细优先，没有明细时列名单。
func FormatWarbondsText(card WarbondsCard) string {
	var b strings.Builder
	b.WriteString("*军需簿*\n")
	if card.Intro != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.Intro))
	}

	if detail := card.Detail; detail != nil {
		fmt.Fprintf(&b, "\n*%s*\n", bot.Escape(detail.Name))
		if detail.English != "" {
			fmt.Fprintf(&b, "%s\n", bot.Escape(detail.English))
		}
		fmt.Fprintf(&b, "页数：%s ｜ 勋章：%s ｜ 价格：%s\n", bot.Escape(detail.Pages), bot.Escape(detail.Medals), bot.Escape(detail.Credits))
		if len(detail.Items) == 0 {
			b.WriteString("这本没有列出装备。\n")
		} else {
			b.WriteString("装备：\n")
			for _, item := range detail.Items {
				fmt.Fprintf(&b, "· %s（%s）", bot.Escape(item.Kind), bot.Escape(item.Name))
				if item.Acquire != "" {
					fmt.Fprintf(&b, " ｜ %s", bot.Escape(item.Acquire))
				}
				b.WriteString("\n")
			}
		}
		if detail.More != "" {
			fmt.Fprintf(&b, "%s\n", bot.Escape(detail.More))
		}
	} else if card.Empty != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.Empty))
	} else {
		for _, book := range card.Books {
			fmt.Fprintf(&b, "\n*%s*\n", bot.Escape(book.Name))
			if book.English != "" {
				fmt.Fprintf(&b, "%s\n", bot.Escape(book.English))
			}
			fmt.Fprintf(&b, "%s ｜ %s ｜ %s ｜ %s\n", bot.Escape(book.Pages), bot.Escape(book.Medals), bot.Escape(book.Credits), bot.Escape(book.Items))
		}
	}
	for _, note := range card.Notes {
		fmt.Fprintf(&b, "%s\n", bot.Escape(note))
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}

// FormatEnemyText 把敌人卡片排成文本：同样只写命中的那一只。
func FormatEnemyText(card EnemyCard) string {
	var b strings.Builder
	b.WriteString("*敌人图鉴*\n")
	if card.Intro != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.Intro))
	}
	if card.Empty != "" {
		fmt.Fprintf(&b, "%s\n", bot.Escape(card.Empty))
	} else if enemy := card.Enemy; enemy != nil {
		fmt.Fprintf(&b, "\n*%s*", bot.Escape(enemy.Name))
		if enemy.English != "" {
			fmt.Fprintf(&b, "（%s）", bot.Escape(enemy.English))
		}
		b.WriteString("\n")
		if len(enemy.Tags) > 0 {
			fmt.Fprintf(&b, "%s\n", bot.Escape(strings.Join(enemy.Tags, " ｜ ")))
		}
		if enemy.Desc != "" {
			fmt.Fprintf(&b, "%s\n", bot.Escape(enemy.Desc))
		}
		if rows := fieldsText(enemy.Rows); rows != "" {
			fmt.Fprintf(&b, "%s\n", bot.Escape(rows))
		}
		if len(enemy.Parts) > 0 {
			fmt.Fprintf(&b, "部位：%d 个\n", len(enemy.Parts))
			for _, part := range enemy.Parts {
				fmt.Fprintf(&b, "· %s 血量 %s ｜ 装甲 %s ｜ 耐久 %s ｜ 对主体 %s\n",
					bot.Escape(part.Name), bot.Escape(plugutil.DefaultText(part.Health, plugutil.DashText)),
					bot.Escape(plugutil.DefaultText(part.Armor, plugutil.DashText)),
					bot.Escape(plugutil.DefaultText(part.Durable, plugutil.DashText)),
					bot.Escape(plugutil.DefaultText(part.ToMain, plugutil.DashText)))
			}
		}
		if len(enemy.Variants) > 0 {
			fmt.Fprintf(&b, "变种：%s\n", bot.Escape(strings.Join(enemy.Variants, "、")))
		}
		if len(enemy.Related) > 0 {
			fmt.Fprintf(&b, "同阵营：%s\n", bot.Escape(enemyLinkNames(enemy.Related)))
		}
		if enemy.Source != "" {
			fmt.Fprintf(&b, "来源：%s\n", bot.Escape(enemy.Source))
		}
	}
	for _, note := range card.Notes {
		fmt.Fprintf(&b, "%s\n", bot.Escape(note))
	}
	b.WriteString(plugutil.DataTimeLine(card.Meta))
	return strings.TrimRight(b.String(), "\n")
}

// enemyLinkNames 把侧栏的邻居列表拼成一行名字（文本回退用）。
func enemyLinkNames(links []EnemyLink) string {
	names := make([]string, 0, len(links))
	for _, link := range links {
		if link.Category != "" {
			names = append(names, link.Name+"（"+link.Category+"）")
			continue
		}
		names = append(names, link.Name)
	}
	return strings.Join(names, "、")
}

// arrowGlyphs 把召唤指令的箭头翻成文本符号：认得出的方向用符号（↑↓←→），
// 认不出的方向显示上游原文——卡片上那一步同理（那边显示原文，不显示空框）。
func arrowGlyphs(steps []ArrowStep) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		if step.Glyph != "" {
			out = append(out, step.Glyph)
			continue
		}
		out = append(out, step.Text)
	}
	return out
}

// fieldsText 把「标签 值」列表拼成一行，用「｜」分隔；列表为空时返回空串。
func fieldsText(fields []Field) string {
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, fmt.Sprintf("%s %s", field.Label, field.Value))
	}
	return strings.Join(parts, " ｜ ")
}
