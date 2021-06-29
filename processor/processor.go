package processor

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"regexp"
	"time"
)

// Each stage represents a certain stage in the pipeline
type PipelineStage int

// When a QueueItem is initially added, it should be of stage Import,
// each time a worker works on the task it should increment it's
// Stage (Title->Omdb->etc..) and set it's Status to 'Pending'
// to allow a worker to pick the item from the Queue
const (
	Import PipelineStage = iota
	Title
	Omdb
	Format
	Finish
)

type Processor struct {
	Config     TPAConfig
	Queue      ProcessorQueue
	WorkerPool *WorkerPool
}

type TitleFormatError struct {
	item    *QueueItem
	message string
}

func (e TitleFormatError) Error() string {
	return fmt.Sprintf("failed to format title(%v) - %v", e.item.Name, e.message)
}

// Instantiates a new processor by creating the
// bare struct, and loading in the configuration
func New() *Processor {
	proc := &Processor{
		Queue: ProcessorQueue{
			Items: make([]*QueueItem, 0),
		},
	}

	proc.Config.LoadConfig()
	proc.WorkerPool = NewWorkerPool()

	return proc
}

// Begin will start the workers inside the WorkerPool
// responsible for the various tasks inside the program
// This includes: HTTP RESTful API (NYI), user interaction (NYI),
// import directory polling, title formatting (NYI), OMDB querying (NYI),
// and the FFMPEG formatting (NYI)
// This method will wait on the WaitGroup attached to the WorkerPool
func (p *Processor) Begin() error {
	importWakeupChan := make(chan int)
	titleWakeupChan := make(chan int)
	//omdbWakeupChan := make(chan int)
	//formatWakupChan := make(chan int)

	tickInterval := time.Duration(p.Config.Format.ImportDirTickDelay * int(time.Second))
	if tickInterval <= 0 {
		log.Panic("Failed to start PollingWorker - TickInterval is non-positive (make sure 'import_polling_delay' is set in your config)")
	}
	go func(source <-chan time.Time, target chan int) {
		for {
			<-source
			target <- 1
		}
	}(time.NewTicker(tickInterval).C, importWakeupChan)

	// Start some workers in the pool to handle various tasks
	p.WorkerPool.NewWorkers(p.Config.Concurrent.Import, "Importer", p.pollingWorkerTask, importWakeupChan, Import)
	p.WorkerPool.NewWorkers(p.Config.Concurrent.Title, "TitleFormatter", p.titleWorkerTask, titleWakeupChan, Title)
	p.WorkerPool.StartWorkers()

	// Kickstart the pipeline
	importWakeupChan <- 1

	// Wait for all to finish
	p.WorkerPool.Wg.Wait()
	return nil
}

// PollInputSource will check the source input directory (from p.Config)
// pass along the files it finds to the p.Queue to be inserted if not present.
func (p *Processor) PollInputSource() (newItemsFound int, err error) {
	log.Printf("Polling input source for new files")
	newItemsFound = 0
	walkFunc := func(path string, dir fs.DirEntry, err error) error {
		if err != nil {
			log.Panicf("PollInputSource failed - %v\n", err.Error())
		}

		if !dir.IsDir() {
			v, err := dir.Info()
			if err != nil {
				log.Panicf("Failed to get FileInfo for path %v - %v\n", path, err.Error())
			}

			if isNew := p.Queue.HandleFile(path, v); isNew {
				log.Printf("Found new file %v\n", path)
				newItemsFound++
			}
		}

		return nil
	}

	err = filepath.WalkDir(p.Config.Format.ImportPath, walkFunc)
	return
}

// FormatTitle accepts a string (title) and reformats it
// based on text-filtering configuration provided by
// the user
func (p *Processor) FormatTitle(item *QueueItem) (string, error) {
	title := item.Name

	matcher := regexp.MustCompile(`([\w.]+)(([SsEe]\d+){2})|(20|19)\d{2}`)
	groups := matcher.FindStringSubmatch(title)

	log.Printf("Regex matches for string %v\nOutput:%#v\n", title, groups)
	if len(groups) == 0 {
		return "", TitleFormatError{item, "regex match failed"}
	}

	return title, nil
}
