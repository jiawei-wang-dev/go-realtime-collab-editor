package permission

type Role string

const (
	RoleOwner Role = "owner"
	RoleWrite Role = "write"
	RoleRead  Role = "read"
)

func (r Role) Valid() bool {
	return r == RoleOwner || r == RoleWrite || r == RoleRead
}

func (r Role) CanRead() bool {
	return r.Valid()
}

func (r Role) CanWrite() bool {
	return r == RoleOwner || r == RoleWrite
}

func (r Role) CanChangePermissions() bool {
	return r == RoleOwner
}
