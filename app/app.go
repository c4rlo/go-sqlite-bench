package app

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func Run(makeDb func(dbfile string) Db) {
	log.SetOutput(os.Stdout)
	log.SetFlags(0)
	exename, _ := os.Executable()
	exename = filepath.Base(exename)
	log.SetPrefix(fmt.Sprintf("%-16s - ", exename))
	log.Print("")
	flag.Parse()
	dbfile := flag.Arg(0)
	if dbfile == "" {
		log.Fatal("dbfile empty, cannot bench")
	}
	// verbose
	const verbose = false
	if verbose {
		log.Printf("dbfile %q", dbfile)
	}
	// run benchmarks
	benchmarks := map[string]bool{
		"simple":     true,
		"complex":    true,
		"many":       true,
		"large":      true,
		"concurrent": true,
	}
	if benchmarks["simple"] {
		benchSimple(dbfile, verbose, makeDb)
	}
	if benchmarks["complex"] {
		benchComplex(dbfile, verbose, makeDb)
	}
	if benchmarks["many"] {
		benchMany(dbfile, verbose, 10, makeDb)
		benchMany(dbfile, verbose, 100, makeDb)
		benchMany(dbfile, verbose, 1_000, makeDb)
	}
	if benchmarks["large"] {
		benchLarge(dbfile, verbose, 50_000, makeDb)
		benchLarge(dbfile, verbose, 100_000, makeDb)
		benchLarge(dbfile, verbose, 200_000, makeDb)
	}
	if benchmarks["concurrent"] {
		benchConcurrent(dbfile, verbose, 2, makeDb)
		benchConcurrent(dbfile, verbose, 4, makeDb)
		benchConcurrent(dbfile, verbose, 8, makeDb)
	}
}

const insertUserSql = "INSERT INTO users(id,created,email,active) VALUES(?,?,?,?)"
const insertArticleSql = "INSERT INTO articles(id,created,userId,text) VALUES(?,?,?,?)"
const insertCommentSql = "INSERT INTO comments(id,created,articleId,text) VALUES(?,?,?,?)"

func initSchema(db Db) {
	db.Exec(
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=1",
		"PRAGMA busy_timeout=5000", // 5s busy timeout
		"CREATE TABLE users ("+
			"id INTEGER PRIMARY KEY NOT NULL,"+
			" created INTEGER NOT NULL,"+ // time.Time
			" email TEXT NOT NULL,"+
			" active INTEGER NOT NULL)", // bool
		"CREATE INDEX users_created ON users(created)",
		"CREATE TABLE articles ("+
			"id INTEGER PRIMARY KEY NOT NULL,"+
			" created INTEGER NOT NULL, "+ // time.Time
			" userId INTEGER NOT NULL REFERENCES users(id),"+
			" text TEXT NOT NULL)",
		"CREATE INDEX articles_created ON articles(created)",
		"CREATE INDEX articles_userId ON articles(userId)",
		"CREATE TABLE comments ("+
			"id INTEGER PRIMARY KEY NOT NULL,"+
			" created INTEGER NOT NULL, "+ // time.Time
			" articleId INTEGER NOT NULL REFERENCES articles(id),"+
			" text TEXT NOT NULL)",
		"CREATE INDEX comments_created ON comments(created)",
		"CREATE INDEX comments_articleId ON comments(articleId)",
	)
}

// Insert 1 million user rows in one database transaction.
// Then query all users once.
func benchSimple(dbfile string, verbose bool, makeDb func(dbfile string) Db) {
	removeDbfiles(dbfile)
	db := makeDb(dbfile)
	defer db.Close()
	initSchema(db)
	// insert users
	var users []User
	base := time.Date(2023, 10, 1, 10, 0, 0, 0, time.Local)
	const nusers = 1_000_000
	for i := 0; i < nusers; i++ {
		users = append(users, NewUser(
			i+1,                                      // id,
			base.Add(time.Duration(i)*time.Minute),   // created,
			fmt.Sprintf("user%08d@example.com", i+1), // email,
			true,                                     // active,
		))
	}
	t0 := time.Now()
	db.InsertUsers("INSERT INTO users(id,created,email,active) VALUES(?,?,?,?)", users)
	insertMillis := millisSince(t0)
	if verbose {
		log.Printf("  insert took %d ms", insertMillis)
	}
	// query users
	t0 = time.Now()
	users = db.FindUsers("SELECT id,created,email,active FROM users ORDER BY id")
	MustBeEqual(len(users), nusers)
	queryMillis := millisSince(t0)
	if verbose {
		log.Printf("  query took %d ms", queryMillis)
	}
	// validate query result
	for i, u := range users {
		MustBeEqual(i+1, u.Id)
		Must(2023 <= u.Created.Year() && u.Created.Year() <= 2025, "wrong created year in %v", u.Created)
		MustBeEqual("user0", u.Email[0:5])
		MustBeEqual(true, u.Active)
	}
	// print results
	label := "simple"
	log.Printf("%20s %12s %12s %12s", label, "insert", "query", "dbsize")
	log.Printf("%20s %12d %12d %12d", label, insertMillis, queryMillis, dbsize(dbfile))
}

// Insert 200 users in one database transaction.
// Then insert 20000 articles (100 articles for each user) in another transaction.
// Then insert 400000 articles (20 comments for each article) in another transaction.
// Then query all users, articles and comments in one big JOIN statement.
func benchComplex(dbfile string, verbose bool, makeDb func(dbfile string) Db) {
	removeDbfiles(dbfile)
	db := makeDb(dbfile)
	defer db.Close()
	initSchema(db)
	const nusers = 200
	const narticlesPerUser = 100
	const ncommentsPerArticle = 20
	if verbose {
		log.Printf("nusers = %d", nusers)
		log.Printf("narticlesPerUser = %d", narticlesPerUser)
		log.Printf("ncommentsPerArticle = %d", ncommentsPerArticle)
	}
	// make users, articles, comments
	var users []User
	var articles []Article
	var comments []Comment
	base := time.Date(2023, 10, 1, 10, 0, 0, 0, time.Local)
	var userId int
	var articleId int
	var commentId int
	for u := 0; u < nusers; u++ {
		userId++
		user := NewUser(
			userId,                                   // Id
			base.Add(time.Duration(u)*time.Minute),   // Created
			fmt.Sprintf("user%08d@example.com", u+1), // Email
			u%2 == 0,                                 // Active
		)
		users = append(users, user)
		for a := 0; a < narticlesPerUser; a++ {
			articleId++
			article := NewArticle(
				articleId, // Id
				base.Add(time.Duration(u)*time.Minute).Add(time.Duration(a)*time.Second), // Created
				userId,         // UserId
				"article text", // Text
			)
			articles = append(articles, article)
			for c := 0; c < ncommentsPerArticle; c++ {
				commentId++
				comment := NewComment(
					commentId,
					base.Add(time.Duration(u)*time.Minute).Add(time.Duration(a)*time.Second).Add(time.Duration(c)*time.Millisecond), // created,
					articleId,
					"comment text", // text,
				)
				comments = append(comments, comment)
			}
		}
	}
	// insert users, articles, comments
	t0 := time.Now()
	db.InsertUsers(insertUserSql, users)
	db.InsertArticles(insertArticleSql, articles)
	db.InsertComments(insertCommentSql, comments)
	insertMillis := millisSince(t0)
	if verbose {
		log.Printf("  insert took %d ms", insertMillis)
	}
	// query users, articles, comments in one big join
	querySql := "SELECT" +
		" users.id, users.created, users.email, users.active," +
		" articles.id, articles.created, articles.userId, articles.text," +
		" comments.id, comments.created, comments.articleId, comments.text" +
		" FROM users" +
		" LEFT JOIN articles ON articles.userId = users.id" +
		" LEFT JOIN comments ON comments.articleId = articles.id" +
		" ORDER BY users.created,  articles.created, comments.created"
	t0 = time.Now()
	users, articles, comments = db.FindUsersArticlesComments(querySql)
	queryMillis := millisSince(t0)
	if verbose {
		log.Printf("  query took %d ms", queryMillis)
	}
	// validate query result
	MustBeEqual(nusers, len(users))
	MustBeEqual(nusers*narticlesPerUser, len(articles))
	MustBeEqual(nusers*narticlesPerUser*ncommentsPerArticle, len(comments))
	for i, user := range users {
		MustBeEqual(i+1, user.Id)
		MustBeEqual(2023, user.Created.Year())
		MustBeEqual("user0", user.Email[0:5])
		MustBeEqual(i%2 == 0, user.Active)
	}
	for i, article := range articles {
		MustBeEqual(i+1, article.Id)
		MustBeEqual(2023, article.Created.Year())
		MustBe(article.UserId >= 1)
		MustBe(article.UserId <= 1+nusers)
		MustBeEqual("article text", article.Text)
		if i > 0 {
			last := articles[i-1]
			MustBe(article.UserId >= last.UserId)
		}
	}
	for i, comment := range comments {
		MustBeEqual(i+1, comment.Id)
		MustBeEqual(2023, comment.Created.Year())
		MustBe(comment.ArticleId >= 1)
		MustBe(comment.ArticleId <= 1+(nusers*narticlesPerUser))
		MustBeEqual("comment text", comment.Text)
		if i > 0 {
			last := comments[i-1]
			MustBe(comment.ArticleId >= last.ArticleId)
		}
	}
	// print results
	label := fmt.Sprintf("complex/%d/%d/%d", nusers, narticlesPerUser, ncommentsPerArticle)
	log.Printf("%20s %12s %12s %12s", label, "insert", "query", "dbsize")
	log.Printf("%20s %12d %12d %12d", label, insertMillis, queryMillis, dbsize(dbfile))
}

// Insert N users in one database transaction.
// Then query all users 1000 times.
// This benchmark is used to simluate a read-heavy use case.
func benchMany(dbfile string, verbose bool, nusers int, makeDb func(dbfile string) Db) {
	removeDbfiles(dbfile)
	db := makeDb(dbfile)
	defer db.Close()
	initSchema(db)
	// insert users
	var users []User
	base := time.Date(2023, 10, 1, 10, 0, 0, 0, time.Local)
	for i := 0; i < nusers; i++ {
		users = append(users, NewUser(
			i+1,                                      // id,
			base.Add(time.Duration(i)*time.Minute),   // created,
			fmt.Sprintf("user%08d@example.com", i+1), // email,
			true,                                     // active,
		))
	}
	t0 := time.Now()
	db.InsertUsers(insertUserSql, users)
	insertMillis := millisSince(t0)
	if verbose {
		log.Printf("  insert took %d ms", insertMillis)
	}
	// query users 1000 times
	t0 = time.Now()
	for i := 0; i < 1000; i++ {
		users = db.FindUsers("SELECT id,created,email,active FROM users ORDER BY id")
		MustBeEqual(len(users), nusers)
	}
	queryMillis := millisSince(t0)
	if verbose {
		log.Printf("  query took %d ms", queryMillis)
	}
	// validate query result
	for i, u := range users {
		MustBeEqual(i+1, u.Id)
		MustBeEqual(2023, u.Created.Year())
		MustBeEqual("user0", u.Email[0:5])
		MustBeEqual(true, u.Active)
	}
	// print results
	label := fmt.Sprintf("many/N=%d", nusers)
	log.Printf("%20s %12s %12s", label, "query", "dbsize")
	log.Printf("%20s %12d %12d", label, queryMillis, dbsize(dbfile))
}

// Insert 10000 users with N bytes of row content.
// Then query all users.
// This benchmark is used to simluate reading of large (gigabytes) databases.
func benchLarge(dbfile string, verbose bool, nsize int, makeDb func(dbfile string) Db) {
	removeDbfiles(dbfile)
	db := makeDb(dbfile)
	defer db.Close()
	initSchema(db)
	// insert user with large emails
	base := time.Date(2023, 10, 1, 10, 0, 0, 0, time.Local)
	const nusers = 10_000
	var users []User
	for i := 0; i < nusers; i++ {
		users = append(users, NewUser(
			i+1,                                    // Id
			base.Add(time.Duration(i)*time.Second), // Created
			strings.Repeat("a", nsize),             // Email
			true,                                   // Active
		))
	}
	db.InsertUsers(insertUserSql, users)
	// query users
	t0 := time.Now()
	users = db.FindUsers("SELECT id,created,email,active FROM users ORDER BY id")
	MustBeEqual(len(users), nusers)
	queryMillis := millisSince(t0)
	if verbose {
		log.Printf("  query took %d ms", queryMillis)
	}
	// validate query result
	for i, u := range users {
		MustBeEqual(i+1, u.Id)
		MustBeEqual(2023, u.Created.Year())
		MustBeEqual("a", u.Email[0:1])
		MustBeEqual(true, u.Active)
	}
	// print results
	label := fmt.Sprintf("large/N=%d", nsize)
	log.Printf("%20s %12s %12s", label, "query", "dbsize")
	log.Printf("%20s %12d %12d", label, queryMillis, dbsize(dbfile))
}

// Insert one million users.
// Then have N goroutines query all users.
// This benchmark is used to simulate concurrent reads.
func benchConcurrent(dbfile string, verbose bool, ngoroutines int, makeDb func(dbfile string) Db) {
	removeDbfiles(dbfile)
	db1 := makeDb(dbfile)
	initSchema(db1)
	// insert many users
	base := time.Date(2023, 10, 1, 10, 0, 0, 0, time.Local)
	const nusers = 1_000_000
	var users []User
	for i := 0; i < nusers; i++ {
		users = append(users, NewUser(
			i+1,                                    // Id
			base.Add(time.Duration(i)*time.Second), // Created
			fmt.Sprintf("user%d@example.com", i+1), // Email
			true,                                   // Active
		))
	}
	db1.InsertUsers(insertUserSql, users)
	db1.Close()
	// query users in N goroutines
	t0 := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < ngoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db := makeDb(dbfile)
			db.Exec(
				"PRAGMA foreign_keys=1",
				"PRAGMA busy_timeout=5000", // 5s busy timeout
			)
			defer db.Close()
			users = db.FindUsers("SELECT id,created,email,active FROM users ORDER BY id")
			MustBeEqual(len(users), nusers)
			// validate query result
			for i, u := range users {
				MustBeEqual(i+1, u.Id)
				MustBeEqual(2023, u.Created.Year())
				MustBeEqual("user", u.Email[0:4])
				MustBeEqual(true, u.Active)
			}
		}()
	}
	// wait for completion
	wg.Wait()
	queryMillis := millisSince(t0)
	if verbose {
		log.Printf("  query took %d ms", queryMillis)
	}
	// print results
	label := fmt.Sprintf("concurrent/N=%d", ngoroutines)
	log.Printf("%20s %12s %12s", label, "query", "dbsize")
	log.Printf("%20s %12d %12d", label, queryMillis, dbsize(dbfile))
}
