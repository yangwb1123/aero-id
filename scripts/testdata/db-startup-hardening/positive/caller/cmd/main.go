package main

func check(db interface{ Ping() error }) error {
	return db.Ping()
}
