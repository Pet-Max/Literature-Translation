package app

import "literature-translation/internal/config"

func (a *App) GetConfigLoadState() config.LoadState {
	s := config.LoadingState()
	if !s.Ready {
		a.configBlocked.Store(true)
	}
	if a.configBlocked.Load() && s.Ready {
		s.Ready = false
		s.Message = "配置文件可读取，请点击重新读取以恢复设置和任务。"
	}
	return s
}

func (a *App) RecoverConfig(action string) (*config.Config, error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	c, err := config.Recover(action)
	if err != nil {
		return nil, err
	}
	a.cfg.Store(c.Clone())
	a.configBlocked.Store(false)
	return c.Clone(), nil
}
