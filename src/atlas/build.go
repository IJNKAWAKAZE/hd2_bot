// 本文件把原始 JSON 翻成卡片能直接用的结构：键名中文化、值翻译、嵌套对象摊平成数值组。
// 规则只有这一份，模板与插件层都不再认识上游键名。
package atlas

import (
	"encoding/json"
	"strings"
	"unicode"
)

// buildWeapon 把一件武器翻成详情。
func (db *DB) buildWeapon(raw rawWeapon) *Entry {
	entry := &Entry{
		Kind:        KindWeapon,
		Name:        str(displayText(raw.Name, raw.NameEn)),
		NameEn:      str(raw.NameEn),
		Category:    categoryName(str(raw.Category)),
		SubCategory: db.value(str(raw.SubCategoryName)),
		Unlock:      db.value(str(displayText(raw.UnlockZh, raw.Unlock))),
		Description: cleanDescription(db.value(str(displayText(raw.DescriptionZh, raw.Description)))),
		Lore:        db.value(str(raw.Lore)),
		Image:       str(raw.Icon),
		Source:      str(raw.SourceURL),
		Traits:      db.valueList(raw.Traits),
		Tips:        db.valueList(raw.Tips),
	}
	entry.Variants = variantNames(raw.Variants)
	entry.Stats, entry.Attacks = db.buildStats(raw.DetailedStats)
	return entry
}

// buildStratagem 把一件战备翻成详情。
func (db *DB) buildStratagem(raw rawStratagem) *Entry {
	entry := &Entry{
		Kind:        KindStratagem,
		Name:        str(displayText(raw.Name, raw.NameEn)),
		NameEn:      str(raw.NameEn),
		Category:    db.value(str(displayText(raw.CategoryZh, raw.Category))),
		Unlock:      db.value(str(displayText(raw.UnlockZh, raw.Unlock))),
		Description: cleanDescription(db.value(str(raw.Description))),
		Image:       str(displayText(raw.Icon, raw.Image)),
		Source:      str(raw.SourcePage),
		Code:        codeSteps(str(raw.Code)),
		CallIn:      db.value(str(raw.CallIn)),
		Cooldown:    db.value(str(raw.Cooldown)),
		Uses:        db.value(str(raw.Uses)),
	}
	entry.Stats, entry.Attacks = db.buildStats(raw.DetailedStats)
	return entry
}

// buildEnemy 把一只敌人翻成详情。阵营不在这里出：卡片用的是我们自己的阵营表与配色，
// 这里只补它没有的那几项（分类、总生命值、伤害类型、踉跄阈值、最低难度、部位数据）。
func (db *DB) buildEnemy(raw rawEnemy) *Entry {
	entry := &Entry{
		Kind:        KindEnemy,
		Name:        str(displayText(raw.NameZh, raw.Name)),
		NameEn:      str(raw.Name),
		Category:    db.value(str(displayText(raw.CategoryZh, raw.Category))),
		Description: cleanDescription(db.value(str(displayText(raw.DescriptionZh, raw.Description)))),
		Image:       str(displayText(raw.ImageThumb, raw.Image)),
		Source:      str(raw.SourcePage),
		Variants:    db.valueList(raw.Variants),
	}
	entry.Health = db.value(str(raw.HealthTotal))
	entry.Damage = db.value(str(displayText(raw.DamageZh, raw.Damage)))
	entry.DamageType = db.value(str(displayText(raw.DamageTypeZh, raw.DamageType)))
	entry.FireMultiplier = db.value(str(displayText(raw.FireMultiplier, raw.FireMultiplierRaw)))
	entry.Stagger = db.value(str(displayText(raw.Stagger, raw.StaggerRaw)))
	entry.Difficulty = db.value(str(displayText(raw.MinDifficulty, raw.MinDifficultyRaw)))

	parts := make([]Part, 0, len(raw.BodyParts))
	for _, raw := range raw.BodyParts {
		part := Part{
			Name:     str(raw.Name),
			Count:    str(raw.Count),
			Health:   db.value(str(raw.Health)),
			Armor:    db.value(str(displayText(raw.ArmorLevelZh, raw.ArmorLevel))),
			Location: db.value(str(displayText(raw.LocationZh, raw.Location))),
			Durable:  str(raw.Durable),
			ToMain:   str(raw.PercentToMain),
			Image:    str(raw.Image),
			Fatal:    isTrue(raw.Fatal),
			Weak:     isTrue(raw.WeakPoint),
		}
		if part.Name == "" {
			part.Name = str(raw.NameEn)
		}
		if part.Name == "" {
			continue
		}
		parts = append(parts, part)
	}
	entry.Parts = parts
	return entry
}

// buildStats 把 detailed_stats 拆成「顶层数值组 + 攻击区块」：
// attacks 与 status_effects 各自成块，其余一层键值合成「详细属性」那一组。
func (db *DB) buildStats(stats rawStats) ([]Group, []Block) {
	if len(stats.Obj.Keys) == 0 {
		return nil, nil
	}
	var groups []Group
	var blocks []Block
	var loose []Cell
	for _, key := range stats.Obj.Keys {
		raw := stats.Obj.Vals[key]
		switch key {
		case "attacks":
			blocks = append(blocks, db.buildAttackBlocks(raw)...)
		case "status_effects":
			if block, ok := db.buildStatusBlock(raw); ok {
				blocks = append(blocks, block)
			}
		default:
			if isNested(raw) {
				// 顶层就给嵌套对象（例如某些战备的炮击参数）时单独成组，读起来比挤在一格里清楚。
				if cells := db.cellsFrom(raw); len(cells) > 0 {
					groups = append(groups, Group{Title: labelOf(key), Cells: cells})
				}
				continue
			}
			loose = append(loose, Cell{Label: labelOf(key), Value: db.valueText(raw)})
		}
	}
	if loose = dropEmptyCells(loose); len(loose) > 0 {
		groups = append([]Group{{Title: "详细属性", Cells: loose}}, groups...)
	}
	return groups, blocks
}

// buildAttackBlocks 把攻击列表翻成区块；没有数值的条目直接跳过，不留空块。
func (db *DB) buildAttackBlocks(raw json.RawMessage) []Block {
	items := decodeObjects(raw)
	blocks := make([]Block, 0, len(items))
	for _, item := range items {
		block := db.buildBlock(item)
		if len(block.Groups) == 0 {
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// buildStatusBlock 把 status_effects 翻成一个区块（标题带上状态名）。
func (db *DB) buildStatusBlock(raw json.RawMessage) (Block, bool) {
	obj, ok := decodeObject(raw)
	if !ok {
		return Block{}, false
	}
	block := db.buildBlock(obj)
	title := "状态效果"
	if status := db.valueText(obj.Vals["status"]); status != "" {
		title += " · " + status
	} else if kind := rawTextOf(obj.Vals["effect_type"]); kind != "" {
		title += " · " + kind
	}
	block.Title = title
	if len(block.Groups) == 0 {
		return Block{}, false
	}
	return block, true
}

// buildBlock 把一个攻击 / 状态对象翻成区块：子对象各成一组，散装键值收进「属性」。
func (db *DB) buildBlock(item orderedObj) Block {
	block := Block{}
	var loose []Cell
	for _, key := range item.Keys {
		value := item.Vals[key]
		switch key {
		case "name":
			// 上游这一列是弹药内部代号（"5.5x50mm FULL METAL JACKET P"），卡片上用不上，直接丢掉。
			continue
		case "type":
			block.Type = componentTypeName(rawTextOf(value))
		default:
			if isNested(value) {
				if cells := db.cellsFrom(value); len(cells) > 0 {
					block.Groups = append(block.Groups, Group{Title: labelOf(key), Cells: cells})
				}
				continue
			}
			loose = append(loose, Cell{Label: labelOf(key), Value: db.valueText(value)})
		}
	}
	if loose = dropEmptyCells(loose); len(loose) > 0 {
		block.Groups = append(block.Groups, Group{Title: "属性", Cells: loose})
	}
	return block
}

// cellsFrom 把一个对象摊平成数值格：里层还是对象时递归展开（键名继续中文化），
// 只带 min/max 的对象收成一格「min-max」。
func (db *DB) cellsFrom(raw json.RawMessage) []Cell {
	obj, ok := decodeObject(raw)
	if !ok {
		return nil
	}
	cells := make([]Cell, 0, len(obj.Keys))
	for _, key := range obj.Keys {
		value := obj.Vals[key]
		if isNested(value) {
			cells = append(cells, db.cellsFrom(value)...)
			continue
		}
		cells = append(cells, Cell{Label: labelOf(key), Value: cellValue(db, key, value)})
	}
	if len(cells) == 0 && len(obj.Keys) > 0 {
		cells = append(cells, Cell{Label: "数值", Value: db.valueText(raw)})
	}
	return dropEmptyCells(cells)
}

// cellValue 取一格的显示值：命中引爆这一列上游存的是弹药内部代号（"120mm HE CANNON ROUND_P1_IE"），
// 显示它只会让人看不懂，这里统一显示「是」——这一列本来就是「会不会炸」的开关。
func cellValue(db *DB, key string, raw json.RawMessage) string {
	text := db.valueText(raw)
	if key == "explosion_on_impact" && text != "" {
		return "是"
	}
	return text
}

// dropEmptyCells 去掉没值的格：上游很多字段是空串或 null，渲染出来就是一排空白行。
func dropEmptyCells(cells []Cell) []Cell {
	out := cells[:0]
	for _, cell := range cells {
		if strings.TrimSpace(cell.Value) == "" {
			continue
		}
		out = append(out, cell)
	}
	return out
}

// valueText 取一个动态 JSON 值的显示文本：字符串翻译后返回、数字与布尔按字面量、
// 数组用「、」连接、min/max 对象收成区间。
func (db *DB) valueText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	switch trimmed[0] {
	case 0x22:
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return ""
		}
		return db.value(text)
	case 0x5b:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text := db.valueText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "、")
	case 0x7b:
		obj, ok := decodeObject(raw)
		if !ok {
			return ""
		}
		if text, ok := minMaxText(obj); ok {
			return text
		}
		parts := make([]string, 0, len(obj.Keys))
		for _, cell := range db.cellsFrom(raw) {
			parts = append(parts, cell.Label+" "+cell.Value)
		}
		return strings.Join(parts, "、")
	default:
		return trimmed
	}
}

// minMaxText 把只有上下限的对象收成「min-max」；不是上下限结构时返回 false。
func minMaxText(obj orderedObj) (string, bool) {
	min, hasMin := obj.Vals["min"]
	max, hasMax := obj.Vals["max"]
	if !hasMin || !hasMax {
		return "", false
	}
	low := strings.TrimSpace(string(min))
	high := strings.TrimSpace(string(max))
	if low == "" || high == "" || low == "null" || high == "null" {
		return "", false
	}
	return low + "-" + high, true
}

// value 翻译一个值：先查内置术语表（整串命中），再做常见词替换；不识别的原样返回。
func (db *DB) value(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if db == nil {
		return trimmed
	}
	if zh, ok := db.terms[trimmed]; ok {
		return zh
	}
	out := trimmed
	for _, pair := range valueReplacements {
		out = strings.ReplaceAll(out, pair[0], pair[1])
	}
	return out
}

// valueList 逐条翻译字符串列表，空串与重复项都丢掉。
func (db *DB) valueList(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		text := db.value(item)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

// rawTextOf 取动态 JSON 的原始文本（不翻译）：型号、单位这类原样展示更准确。
func rawTextOf(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if trimmed[0] == 0x22 {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return ""
		}
		return strings.TrimSpace(text)
	}
	return trimmed
}

// str 取结构体字段的文本。
func str(text flexString) string { return strings.TrimSpace(string(text)) }

// displayText 在中文缺位时退回英文：绝不返回空文本。
func displayText(zh, en flexString) flexString {
	if text := str(zh); text != "" {
		return flexString(text)
	}
	return flexString(str(en))
}

// variantNames 取变体名（只留名字，解锁条件在详情卡上没地方放）。
func variantNames(items []rawVariant) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if name := str(item.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// codeSteps 把战备的呼叫指令串（例如 "RRDLRD"）拆成方向：参照站用的是方向首字母缩写。
func codeSteps(code string) []string {
	steps := make([]string, 0, len(code))
	for _, ch := range strings.ToUpper(code) {
		switch ch {
		case 0x55:
			steps = append(steps, "up")
		case 0x44:
			steps = append(steps, "down")
		case 0x4C:
			steps = append(steps, "left")
		case 0x52:
			steps = append(steps, "right")
		}
	}
	return steps
}

// decodeObject 解析一个 JSON 对象；不是对象或没有键时返回 false。
func decodeObject(raw json.RawMessage) (orderedObj, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed[0] != 0x7b {
		return orderedObj{}, false
	}
	var obj orderedObj
	if err := json.Unmarshal(raw, &obj); err != nil {
		return orderedObj{}, false
	}
	if len(obj.Keys) == 0 {
		return orderedObj{}, false
	}
	return obj, true
}

// decodeObjects 解析一个 JSON 对象数组；解析不了时返回 nil。
func decodeObjects(raw json.RawMessage) []orderedObj {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed[0] != 0x5b {
		return nil
	}
	var items []orderedObj
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return items
}

// isObject 判断一个 JSON 值是不是对象。
func isObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed[0] == 0x7b
}

// isNested 判断一个 JSON 值是不是「该摊平成一组」的对象：上下限结构不算（它自己就是一格）。
func isNested(raw json.RawMessage) bool {
	if !isObject(raw) {
		return false
	}
	obj, ok := decodeObject(raw)
	if !ok {
		return false
	}
	_, isMinMax := minMaxText(obj)
	return !isMinMax
}

// cleanDescription 过滤掉不是描述的描述：上游少数条目这一列填的是版本号与日期
// （实测毒气榴弹就是 "1.006.202 2026-04-28"），显示在卡片上只会让人以为是乱码。
func cleanDescription(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for _, r := range trimmed {
		if unicode.IsLetter(r) {
			return trimmed
		}
	}
	return ""
}

// isTrue 判断上游的布尔值（有 true，也见过 "yes"）。
func isTrue(value flexString) bool {
	switch strings.ToLower(str(value)) {
	case "true", "yes", "1":
		return true
	default:
		return false
	}
}

// normalize 把名字折叠成索引键：忽略大小写，去掉空格与标点，中文原样保留。
func normalize(text string) string {
	var builder strings.Builder
	for _, ch := range strings.ToLower(text) {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			builder.WriteRune(ch)
		}
	}
	return builder.String()
}
