package models

type GroupInfo struct {
	Name          string
	GroupType     string
	ModelRedirect map[string]string
	TestModel     string
}

type GroupManager interface {
	GetAllGroups() []GroupInfo
}
