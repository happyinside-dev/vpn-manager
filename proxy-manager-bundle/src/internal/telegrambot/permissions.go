package telegrambot

import "proxy-dev/vpn-manager/internal/users"

func isManagedCommand(cmd string) bool {
	switch cmd {
	case "/delete", "/enable", "/disable", "/rotate", "/rotate_token", "/invite", "/unbind", "/links", "/traffic":
		return true
	}
	return false
}
func (b *Bot) canManage(id int64) bool {
	u, err := users.FindByTelegramIDFile(b.cfg.UsersFile, id)
	return err == nil && u.Enabled && u.CanManageUsers
}
func (b *Bot) owns(id int64, name string) bool {
	u, err := users.FindByNameFile(b.cfg.UsersFile, name)
	return err == nil && id != 0 && u.CreatedBy == id && b.canManage(id)
}
func (b *Bot) authorizeManagement(id int64, cmd string, args []string) bool {
	if b.isMaster(id) {
		return true
	}
	if !b.canManage(id) {
		return false
	}
	if cmd == "/users" {
		return len(args) == 0
	}
	if cmd == "/add" {
		return true
	}
	return isManagedCommand(cmd) && len(args) > 0 && b.owns(id, args[0])
}

func (b *Bot) actorRole(id int64) string {
	if b.isMaster(id) {
		return "master"
	}
	return "delegate"
}
