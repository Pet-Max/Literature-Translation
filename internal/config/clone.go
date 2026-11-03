package config

// Clone owns all slices and draft pointers, so published configurations are immutable.
func (c *Config) Clone() *Config {
	if c == nil {
		return Default()
	}
	n := *c
	n.Glossary = append([]Term{}, c.Glossary...)
	n.UserFonts = append([]UserFont{}, c.UserFonts...)
	if c.GlossaryRows != nil {
		rows := append([]Term{}, (*c.GlossaryRows)...)
		n.GlossaryRows = &rows
	}
	n.Rules.Presets = append([]RulePreset{}, c.Rules.Presets...)
	for i := range n.Rules.Presets {
		if p := c.Rules.Presets[i].DraftText; p != nil {
			value := *p
			n.Rules.Presets[i].DraftText = &value
		}
	}
	return &n
}
