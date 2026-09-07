package app

func build() {
	_ = aerovault.New()
	_ = aerovault.NewWithCapabilities()
	_ = snaplink.NewWithCapabilities()
}
