package p9pnew

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"time"

	"golang.org/x/net/context"
)

// Serve the 9p session over the provided network connection.
func Serve(ctx context.Context, conn net.Conn, session Session) {
	s := &server{
		ctx:     ctx,
		conn:    conn,
		session: session,
	}

	s.run()
}

type server struct {
	ctx     context.Context
	session Session
	conn    net.Conn
	closed  chan struct{}
}

func (s *server) run() {
	brd := bufio.NewReader(s.conn)
	dec := &decoder{brd}
	bwr := bufio.NewWriter(s.conn)
	enc := &encoder{bwr}

	tags := map[Tag]*Fcall{} // active requests

	log.Println("server.run()")
	for {
		select {
		case <-s.ctx.Done():
			log.Println("server: shutdown")
			return
		case <-s.closed:
		default:
		}

		// NOTE(stevvooe): For now, we only provide a single request at a time
		// handler. We can refactor this to take requests off the wire as
		// quickly as they arrive and dispatch in parallel to session.

		log.Println("server:", "wait")
		fcall := new(Fcall)
		if err := dec.decode(fcall); err != nil {
			log.Println("server decoding fcall:", err)
			continue
		}

		log.Println("server:", "message", fcall)

		if _, ok := tags[fcall.Tag]; ok {
			if err := enc.encode(&Fcall{
				Type: Rerror,
				Tag:  fcall.Tag,
				Message: &MessageRerror{
					Ename: ErrDuptag.Error(),
				},
			}); err != nil {
				log.Println("server:", err)
			}
			bwr.Flush()
			continue
		}
		tags[fcall.Tag] = fcall

		resp, err := s.handle(fcall)
		if err != nil {
			log.Println("server:", err)
			continue
		}

		if err := enc.encode(resp); err != nil {
			log.Println("server:", err)
			continue
		}
		bwr.Flush()

	}
}

// handle responds to an fcall using the session. An error is only returned if
// the handler cannot proceed. All session errors are returned as Rerror.
func (s *server) handle(req *Fcall) (*Fcall, error) {
	const timeout = 30 * time.Second // TODO(stevvooe): Allow this to be configured.
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	var resp *Fcall
	switch req.Type {
	case Tattach:
		reqmsg, ok := req.Message.(*MessageTattach)
		if !ok {
			return nil, fmt.Errorf("bad message: %v message=%#v", req, req.Message)
		}

		qid, err := s.session.Attach(ctx, reqmsg.Fid, reqmsg.Afid, reqmsg.Uname, reqmsg.Aname)
		if err != nil {
			return nil, err
		}

		resp = &Fcall{
			Type: Rattach,
			Tag:  req.Tag,
			Message: &MessageRattach{
				Qid: qid,
			},
		}
	case Twalk:
		reqmsg, ok := req.Message.(*MessageTwalk)
		if !ok {
			return nil, fmt.Errorf("bad message: %v message=%#v", req, req.Message)
		}

		// TODO(stevvooe): This is one of the places where we need to manage
		// fid allocation lifecycle. We need to reserve the fid, then, if this
		// call succeeds, we should alloc the fid for future uses. Also need
		// to interact correctly with concurrent clunk and the flush of this
		// walk message.
		qids, err := s.session.Walk(ctx, reqmsg.Fid, reqmsg.Newfid, reqmsg.Wnames...)
		if err != nil {
			return nil, err
		}

		resp = newFcall(&MessageRwalk{
			Qids: qids,
		})
	}

	if resp == nil {
		resp = newFcall(&MessageRerror{
			Ename: "unknown message type",
		})
	}

	resp.Tag = req.Tag
	return resp, nil
}
