module go-mahjong-server

go 1.14

replace (
	golang.org/x/crypto => github.com/golang/crypto v0.0.0-20181001203147-e3636079e1a4
	golang.org/x/net => github.com/golang/net v0.0.0-20180926154720-4dfa2610cdf3
	golang.org/x/sys => github.com/golang/sys v0.0.0-20180928133829-e4b3c5e90611
	golang.org/x/text => github.com/golang/text v0.3.0
)

require (
	github.com/denisenkom/go-mssqldb v0.9.0 // indirect
	github.com/go-sql-driver/mysql v1.4.0
	github.com/go-xorm/core v0.6.0
	github.com/go-xorm/xorm v0.7.0
	github.com/gorilla/mux v1.8.0
	github.com/lib/pq v1.9.0 // indirect
	github.com/lonng/nano v0.5.1-0.20201210024405-e51e7f3a2372
	github.com/lonng/nex v1.4.1
	github.com/mattn/go-sqlite3 v1.14.6 // indirect
	github.com/pborman/uuid v1.2.1
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.6.0
	github.com/spf13/viper v1.2.1
	github.com/urfave/cli v1.20.1-0.20190203184040-693af58b4d51
	github.com/xxtea/xxtea-go v0.0.0-20170828040851-35c4b17eecf6
	github.com/ziutek/mymysql v1.5.4 // indirect
	golang.org/x/crypto v0.0.0-20190510104115-cbcb75029529
	golang.org/x/text v0.3.2
)
