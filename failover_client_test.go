package modbusone

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func connectMockClients(t *testing.T, slaveID byte) (*FailoverRTUClient, *FailoverRTUClient, *counter, *counter, *counter) {

	//pipes
	ra, wa := io.Pipe() //client a
	rb, wb := io.Pipe() //client b
	rc, wc := io.Pipe() //server

	//everyone writes to everyone else
	wfa := io.MultiWriter(wb, wc) //write from a, etc...
	wfb := io.MultiWriter(wa, wc)
	wfc := io.MultiWriter(wa, wb)

	ca := NewFailoverConn(newMockSerial(ra, wfa), false, true) //client a connection
	cb := NewFailoverConn(newMockSerial(rb, wfb), true, true)  //client b connection
	sc := newMockSerial(rc, wfc)                               //server connection

	clientA := NewFailoverRTUClient(ca, false, slaveID)
	clientB := NewFailoverRTUClient(cb, true, slaveID)
	server := NewRTUServer(sc, slaveID)

	//faster timeouts during testing
	clientA.SetServerProcessingTime(time.Second / 50)
	clientB.SetServerProcessingTime(time.Second / 50)
	setDelays(ca)
	setDelays(cb)

	_, shA, countA := newTestHandler("client A", t)
	countA.Stats = ca.Stats()
	_, shB, countB := newTestHandler("client B", t)
	countB.Stats = cb.Stats()
	holdingRegistersC, shC, countC := newTestHandler("server", t)
	countC.Stats = sc.Stats()
	for i := range holdingRegistersC {
		holdingRegistersC[i] = uint16(i + 1<<8)
	}

	go clientA.Serve(shA)
	go clientB.Serve(shB)
	go server.Serve(shC)

	primaryActiveClient = func() bool {
		if ca.isActive {
			return true
		}
		ca.isActive = true
		ca.misses = ca.MissesMax
		return false
	}

	return clientA, clientB, countA, countB, countC
}

//return if primary is active, or set it to active is not already
var primaryActiveClient func() bool

func TestFailoverClient(t *testing.T) {
	//t.Skip()
	//errorRate := 3  //number of failures allowed for fuzzyness of each test
	//testCount := 20 //number of repeats of each test

	id := byte(0x77)
	clientA, clientB, countA, countB, countC := connectMockClients(t, id)
	exCount := counter{Stats: &Stats{}}
	resetCounts := func() {
		exCount.reset()
		countA.reset()
		countB.reset()
		countC.reset()
	}

	type tc struct {
		fc   FunctionCode
		size uint16
	}
	testCases := []tc{
		{FcWriteSingleRegister, 20},
		{FcWriteMultipleRegisters, 20},
		{FcReadHoldingRegisters, 5},
	}

	_ = os.Stdout
	//SetDebugOut(os.Stdout)
	defer func() { SetDebugOut(nil) }()

	t.Run("cold start", func(t *testing.T) {
		reqs, err := MakePDURequestHeadersSized(FcReadHoldingRegisters, 0, 1, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; /*MissesMax*/ i++ {
			//activates client
			DoTransactions(clientA, id, reqs)
			DoTransactions(clientB, id, reqs)
		}
		if !primaryActiveClient() {
			t.Fatal("primaray servers should be active")
		}
		time.Sleep(time.Second / 10)
		resetCounts()
	})
	//primaryActiveClient()

	for i, ts := range testCases {
		t.Run(fmt.Sprintf("normal %v fc:%v size:%v", i, ts.fc, ts.size), func(t *testing.T) {
			reqs, err := MakePDURequestHeadersSized(ts.fc, 0, ts.size, 1, nil)
			if err != nil {
				t.Fatal(err)
			}
			go DoTransactions(clientB, id, reqs)
			DoTransactions(clientA, id, reqs)
			time.Sleep(time.Second / 100 * time.Duration(ts.size))
			if ts.fc.IsReadToServer() {
				exCount.writes += int(ts.size)
			} else {
				exCount.reads += int(ts.size)
			}
			if exCount.reads != countC.writes || exCount.writes != countC.reads {
				t.Error("server counter     ", countC)
				t.Error("expected (inverted)", exCount)
				t.Error(countC.Stats)
			}
			if exCount.reads != countA.reads || exCount.writes != countA.writes {
				t.Error("client a counter", countA)
				t.Error("expected        ", exCount)
				t.Error(countA.Stats)
			}
			if exCount.reads != countB.reads || exCount.writes != countB.writes {
				t.Error("client b counter", countB)
				t.Error("expected        ", exCount)
				t.Error(countB.Stats)
			}
			resetCounts()
		})
	}
}
