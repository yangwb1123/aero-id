package domain

func CanReadFile(actor string, f *File) bool {
	return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}

func CanReadMessage(actor string, m *Message) bool {
	return m.FromUID == actor || m.ToUID == actor
}
