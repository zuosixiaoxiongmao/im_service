package main
import "net"
import "log"
import "sync"
import "time"

const CLIENT_TIMEOUT = 20
type Client struct {
    tm time.Time
    wt chan *Message
    uid int64
    conn *net.TCPConn
    unacks []*Message
    mutex sync.Mutex
}

func NewClient(conn *net.TCPConn) *Client {
    client := new(Client)
    client.conn = conn
    client.wt = make(chan *Message)
    client.unacks = make([]*Message, 0, 4)
    return client
}

func (client *Client) Read() {
    for {
        client.conn.SetDeadline(time.Now().Add(CLIENT_TIMEOUT*time.Second))
        msg := ReceiveMessage(client.conn)
        if msg == nil {
            route.RemoveClient(client)
            if client.uid > 0 {
                cluster.RemoveClient(client.uid)
            }
            client.wt <- nil
            break
        }
        log.Println("msg:", msg.cmd)
        if msg.cmd == MSG_AUTH {
            client.HandleAuth(msg.body.(*Authentication))
        } else if msg.cmd == MSG_IM {
            client.HandleIMMessage(msg.body.(*IMMessage), msg.seq)
        } else if msg.cmd == MSG_GROUP_IM {
            client.HandleGroupIMMessage(msg.body.(*IMMessage), msg.seq)
        } else if msg.cmd == MSG_ACK {
            client.HandleACK(msg.body.(MessageACK))
        } else if msg.cmd == MSG_HEARTBEAT {
            
        }
    }
}
    
func (client *Client) SendOfflineMessage() {
    go func() {
        c := storage.LoadOfflineMessage(client.uid)
        if c != nil {
            for m := range c {
                client.wt <- m
            }
            storage.ClearOfflineMessage(client.uid)
        }
    }()
}

func (client *Client) ResetClient(uid int64) {
	//单点登录
    c := route.FindClient(client.uid)
    if c != nil {
        c.wt <- &Message{cmd:MSG_RST}
    }
}

func (client *Client) HandleAuth(login *Authentication) {
    client.tm = time.Now()
    client.uid = login.uid
    log.Println("auth:", login.uid)
    msg := &Message{cmd:MSG_AUTH_STATUS, body:&AuthenticationStatus{0}}
    client.wt <- msg

    client.ResetClient(client.uid)

    route.AddClient(client)
    cluster.AddClient(client.uid, int32(client.tm.Unix()))
    client.SendOfflineMessage()
}

func (client *Client) HandleIMMessage(msg *IMMessage, seq int) {
    other := route.FindClient(msg.receiver)
    if other != nil {
        other.wt <- &Message{cmd:MSG_IM, body:msg}
    } else {
        peer := route.FindPeerClient(msg.receiver)
        if peer != nil {
            peer.wt <- &Message{cmd:MSG_IM, body:msg}
        } else {
            storage.SaveOfflineMessage(msg.receiver, &Message{cmd:MSG_IM, body:msg})
        }
    }
    client.wt <- &Message{cmd:MSG_ACK, body:MessageACK(seq)}
}

func (client *Client) HandleGroupIMMessage(msg *IMMessage, seq int) {
    group := group_manager.FindGroup(msg.receiver)
    if group == nil {
        log.Println("can't find group:", msg.receiver)
        return
    }
    peers := make(map[*PeerClient]struct{})
    for member := range group.Members() {
        other := route.FindClient(member)
        if other != nil {
            other.wt <- &Message{cmd:MSG_GROUP_IM, body:msg}
        } else {
            peer := route.FindPeerClient(member)
            if peer != nil {
                peers[peer] = struct{}{}
            } else {
                storage.SaveOfflineMessage(member, &Message{cmd:MSG_GROUP_IM, body:msg})           
            }
        }
    }
    for peer, _ := range peers {
        peer.wt <- &Message{cmd:MSG_GROUP_IM, body:msg}
    }
    client.wt <- &Message{cmd:MSG_ACK, body:MessageACK(seq)}
}

func (client *Client) HandleACK(ack MessageACK) {
    client.RemoveUnAckMessage(ack)
}

func (client *Client) RemoveUnAckMessage(ack MessageACK) {
    client.mutex.Lock()
    defer client.mutex.Unlock()

    pos := -1
    for i, msg := range client.unacks {
        if msg.seq == int(ack) {
            pos = i
            break
        }
    }
    client.unacks = client.unacks[pos+1:]
    if pos == -1 {
        log.Println("invalid ack seq:", ack)
    }
    log.Println("remove unack msg:", len(client.unacks))
}

func (client *Client) AddUnAckMessage(msg *Message) {
    client.mutex.Lock()
    defer client.mutex.Unlock()
    client.unacks = append(client.unacks, msg)
}

func (client *Client) SaveUnAckMessage() {
    client.mutex.Lock()
    defer client.mutex.Unlock()
    for _, msg := range client.unacks {
        storage.SaveOfflineMessage(client.uid, msg)
    }
}

func (client *Client) Write() {
    seq := 0
    rst := false
    for {
        msg := <- client.wt
        if msg == nil {
            client.SaveUnAckMessage()
            client.conn.Close()
            log.Println("socket closed")
            break
        }
        seq++
        msg.seq = seq
        if msg.cmd == MSG_IM || msg.cmd == MSG_GROUP_IM {
            client.AddUnAckMessage(msg)
        }

		if rst {
			continue
		}
        SendMessage(client.conn, msg)
        if msg.cmd == MSG_RST {
            client.conn.Close()
            rst = true
        }
    }
}

func (client *Client) Run() {
    go client.Write()
    go client.Read()
}