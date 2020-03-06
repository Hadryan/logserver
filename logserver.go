package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	pb "github.com/ultram4rine/logserver/proto"

	"github.com/BurntSushi/toml"
	_ "github.com/ClickHouse/clickhouse-go"
	"github.com/jmoiron/sqlx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"gopkg.in/alecthomas/kingpin.v2"
)

var config struct {
	Port string `toml:"listen_port"`
	DB   db     `toml:"db"`
}

type db struct {
	Host string `toml:"host"`
	Name string `toml:"name"`
	User string `toml:"user"`
	Pass string `toml:"pass"`
}

var confpath = kingpin.Flag("conf", "Path to config file.").Short('c').Default("logserver.conf.toml").String()

func main() {
	kingpin.Parse()

	if _, err := toml.DecodeFile(*confpath, &config); err != nil {
		log.Fatalf("Error decoding config file from %s", *confpath)
	}

	db, err := sqlx.Connect("clickhouse", fmt.Sprintf("%s?username=%s&password=%s&database=%s", config.DB.Host, config.DB.User, config.DB.Pass, config.DB.Name))
	if err != nil {
		log.Fatalf("Error connecting to database: %s", err)
	}
	defer db.Close()

	listener, err := net.Listen("tcp", ":"+config.Port)
	if err != nil {
		grpclog.Fatalf("failed to listen: %v", err)
	}

	opts := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(opts...)

	pb.RegisterLogServer(grpcServer, &server{DB: db})
	grpcServer.Serve(listener)
}

type server struct {
	DB *sqlx.DB
}

func (s *server) GetSimilarSwitches(c context.Context, request *pb.SwName) (response *pb.Switches, err error) {
	rows, err := s.DB.QueryxContext(c, "SELECT DISTINCT sw_name, sw_ip FROM switchlogs WHERE sw_name LIKE ?", request.GetName()+"%")
	if err != nil {
		return nil, err
	}

	var switches *pb.Switches
	for rows.Next() {
		var s *pb.Switch
		if err = rows.Scan(&s.Name, &s.IP); err != nil {
			return nil, err
		}

		switches.Switch = append(switches.Switch, s)
	}
	if rows.Err() != nil {
		return nil, err
	}

	return switches, nil
}

func (s *server) GetDHCPLog(c context.Context, request *pb.DHCPLogEntry) (response *pb.DHCPLogs, err error) {
	timeFrom, timeTo, err := parseTime(request.GetFrom(), request.GetTo())
	if err != nil {
		return nil, err
	}

	var mhex []byte
	binary.BigEndian.PutUint64(mhex, request.GetMAC())
	mcvt := net.HardwareAddr(mhex).String()

	rows, err := s.DB.QueryxContext(c, "SELECT ts, message, ip FROM dhcp.events WHERE mac = MACStringToNum(?) AND ts > ? AND ts < ? ORDER BY ts DESC", mcvt, timeFrom, timeTo)
	if err != nil {
		return nil, err
	}

	var logs *pb.DHCPLogs
	for rows.Next() {
		var l *pb.DHCPLog
		if err = rows.Scan(&l.Timestamp, &l.Message, &l.Ip); err != nil {
			return nil, err
		}

		logs.Log = append(logs.Log, l)
	}
	if rows.Err() != nil {
		return nil, err
	}

	return logs, nil
}

func (s *server) GetSwitchLog(c context.Context, request *pb.SwitchLogEntry) (response *pb.SwitchLogs, err error) {
	timeFrom, timeTo, err := parseTime(request.GetFrom(), request.GetTo())
	if err != nil {
		return nil, err
	}

	rows, err := s.DB.QueryxContext(c, "SELECT ts_remote, log_msg FROM switchlogs WHERE sw_name = ? AND ts_local > ? AND ts_local < ? ORDER BY ts_local DESC", request.GetName(), timeFrom, timeTo)
	if err != nil {
		return nil, err
	}

	var logs *pb.SwitchLogs
	for rows.Next() {
		var l *pb.SwitchLog
		if err = rows.Scan(&l.Ts, &l.Message); err != nil {
			return nil, err
		}

		logs.Log = append(logs.Log, l)
	}
	if rows.Err() != nil {
		return nil, err
	}

	return logs, nil
}

func parseTime(fromStr, toStr string) (time.Time, time.Time, error) {
	from, err := strconv.Atoi(fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := strconv.Atoi(toStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	fromDuration := time.Minute * -time.Duration(from)
	toDuration := time.Minute * -time.Duration(to)

	timeFrom := time.Now().Add(fromDuration)
	timeTo := time.Now().Add(toDuration)

	return timeFrom, timeTo, nil
}
