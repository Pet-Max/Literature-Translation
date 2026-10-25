package pipeline

import "literature-translation/internal/config"

// IronRules 系统默认翻译规则（现收归 config.DefaultRules，作为规则预设体系
// 的「默认」底稿；保留此别名以免破坏既有引用与前端 App.IronRules 绑定）。
const IronRules = config.DefaultRules
