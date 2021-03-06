package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/astaxie/beego/logs"
	_ "github.com/go-sql-driver/mysql"
	uuid "github.com/satori/go.uuid"
	"log"
	"net"
	"net/http"
	"time"
)

type Msg struct {
	GroupId string
	Type    string
	Command string
	TxCount int
	IsEnd   bool
}
type TxConnection struct {
	Tx      *sql.Tx
	Msg     Msg
	IsStart bool
}

//代理开启全局事务，TM发送begin
func (tx *TxConnection) Begin() error {
	fmt.Println("代理开启全局事务")
	tcpAddr, err := net.ResolveTCPAddr("tcp4", "localhost:7778")
	if err != nil {
		log.Fatal(err)
		return err
	}

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		log.Fatal(err)
		return err
	}

	msg := Msg{
		GroupId: tx.Msg.GroupId,
		TxCount: tx.Msg.TxCount,
		Command: "create",
	}
	bytes, err := json.Marshal(&msg)
	if err != nil {
		log.Fatal(err)
		return err
	}

	_, err = conn.Write(bytes)
	if err != nil {
		log.Fatal(err)
		return err
	}

	return nil
}

func (tx *TxConnection) Commit() error {
	fmt.Println("代理commit")
	tcpAddr, err := net.ResolveTCPAddr("tcp4", "localhost:7778")
	if err != nil {
		logs.Error(err)
		tx.Tx.Rollback()
		return err
	}

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		logs.Error(err)
		tx.Tx.Rollback()
		conn.Close()
		return err
	}

	msg := Msg{
		GroupId: tx.Msg.GroupId,
		Type:    tx.Msg.Type,
		Command: "add",
		TxCount: tx.Msg.TxCount,
		IsEnd:   tx.Msg.IsEnd,
	}

	if tx.Msg.Command != "" {
		msg.Command = tx.Msg.Command
	}

	bytes, err := json.Marshal(&msg)
	if err != nil {
		logs.Error(err)
		tx.Tx.Rollback()
		conn.Close()
		return err
	}

	_, err = conn.Write(bytes)
	if err != nil {
		logs.Error(err)
		tx.Tx.Rollback()
		conn.Close()
		return err
	}

	if tx.IsStart {
		for {
			b := make([]byte, 1024)
			n, err := conn.Read(b)
			if err != nil {
				logs.Error(err)
				tx.Tx.Rollback()
				conn.Close()
				return err
			}
			reseiveMsg := make([]byte, n)
			reseiveMsg = b[:n]
			msg := Msg{}
			json.Unmarshal(reseiveMsg, &msg)
			if msg.Command == "commit" {
				//收到事务管理器通知提交
				fmt.Println(msg.Command)
				tx.Tx.Commit()
				conn.Close()
				return err
			} else if msg.Command == "rollback" {
				//收到事务管理器通知回滚
				fmt.Println(msg.Command)
				tx.Tx.Rollback()
				conn.Close()
				return err
			}

		}
	} else {
		go func() {
			for {
				b := make([]byte, 1024)
				n, err := conn.Read(b)
				if err != nil {
					logs.Error(err)
					tx.Tx.Rollback()
					conn.Close()
					return
				}
				reseiveMsg := make([]byte, n)
				reseiveMsg = b[:n]
				msg := Msg{}
				json.Unmarshal(reseiveMsg, &msg)
				if msg.Command == "commit" {
					//收到事务管理器通知提交
					fmt.Println(msg.Command)
					tx.Tx.Commit()
					conn.Close()
					return
				} else if msg.Command == "rollback" {
					//收到事务管理器通知回滚
					fmt.Println(msg.Command)
					tx.Tx.Rollback()
					conn.Close()
					return
				}

			}
		}()
	}

	return nil
}

func (tx *TxConnection) Rollback() error {
	fmt.Println("代理rollback")
	return tx.Tx.Rollback()
}

// 插入数据事务
func InsertTx(db *sql.Tx) error {
	stmt, err := db.Prepare("INSERT INTO user(name, age) VALUES(?, ?);")
	if err != nil {
		log.Fatal(err)
		return err
	}
	res, err := stmt.Exec("sssssssss", 1)
	if err != nil {
		logs.Error(err)
		return err
	}
	lastId, err := res.LastInsertId()
	if err != nil {
		logs.Error(err)
		return err
	}
	rowCnt, err := res.RowsAffected()
	if err != nil {
		logs.Error(err)
		return err
	}
	fmt.Printf("ID=%d, affected=%d\n", lastId, rowCnt)

	//return errors.New("test")
	return nil
}

func TMBegin(db *sql.DB, isTM bool, txCount ...int) (*TxConnection, error) {

	tx, err := db.Begin()
	if err != nil {
		logs.Error(err)
		return nil, err
	}
	txConnection := &TxConnection{
		Tx: tx,
	}
	//事务组ID
	//txConnection.Msg.GroupId = "0001"

	//是否为事务发起者TM
	if isTM {
		//事务发起者生成事务组ID
		u4, _ := uuid.NewV4()
		txConnection.Msg.GroupId =u4.String()

		//分支事务总个数
		if len(txCount) == 0 {
			return nil, errors.New("事务发起者请设置分支事务数量")		}

		if txCount[0] <= 0 {
			return nil, errors.New("分支事务数不能小于等于0")
		}
		txConnection.Msg.TxCount = txCount[0]

		//事务发起者标识
		txConnection.IsStart = true

		err = txConnection.Begin()

		if err != nil {
			logs.Error(err)
			return nil, err
		}
	}

	return txConnection, nil
}

func RMRollback(tx *TxConnection, isEnd bool) {
	//tx.Msg.TxCount = count
	tx.Msg.IsEnd = isEnd
	tx.Msg.Type = "rollback"
}

func RMCommit(tx *TxConnection, isEnd bool) {
	//tx.Msg.TxCount = count
	tx.Msg.IsEnd = isEnd
	tx.Msg.Type = "commit"
}

//事务发起者取消事务
func TMCancel(tx *TxConnection) {
	tx.Msg.Command = "cancel"
}

//超时取消全局事务，并回滚
func timeout(tx *TxConnection) {
	time.Sleep(5 * time.Second)
	TMCancel(tx)
}

func main() {

	http.HandleFunc("/rm2", tm1)
	log.Fatal(http.ListenAndServe(":1001", nil))

}

func tm1(w http.ResponseWriter, r *http.Request) {
	vars := r.URL.Query()
	groupId, ok := vars["groupId"]
	if !ok {
		logs.Error(errors.New("no groupId"))
		w.Write([]byte("error"))
		return
	}

	db, err := sql.Open("mysql", "root:123456@/test2")
	defer db.Close()
	if err != nil {
		logs.Error(err)
		w.Write([]byte("error"))
		return
	}

	//开启全局事务
	txConnection, err := TMBegin(db, false) //非事务发起者
	txConnection.Msg.GroupId = groupId[0]

	if err != nil {
		logs.Error(err)
		w.Write([]byte("error"))
		return
	}

	//执行本地事务
	err = InsertTx(txConnection.Tx)

	if err != nil {
		//设置回滚消息
		RMRollback(txConnection, false)
	} else {
		//设置提交消息
		RMCommit(txConnection, false)
	}

	//提交全局事务
	err = txConnection.Commit()

	if err != nil {
		logs.Error(err)
		w.Write([]byte("error"))
		return
	}
}
