package app

func check(db interface{ PingContext() error }) error {
	return db.PingContext()
}
