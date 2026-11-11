package pipeline

import "literature-translation/internal/config"

// IronRules 「物理」预设的出厂规则（现收归 config.DefaultRules；
// 保留此别名以免破坏既有引用与前端 App.IronRules 绑定）。
const IronRules = config.DefaultRules
