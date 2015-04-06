package ivy

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"sort"
	"strconv"
	"sync"
)

type Record interface {
	Transform()
}

type DB struct {
	path       string
	rwLocks    map[string]*sync.RWMutex
	tagIndexes map[string]map[string][]string
}

func OpenDB(dbPath string) (*DB, error) {
	db := &DB{}

	db.path = dbPath

	db.rwLocks = make(map[string]*sync.RWMutex)

	db.tagIndexes = make(map[string]map[string][]string)

	files, _ := ioutil.ReadDir(db.path)

	for _, file := range files {
		if file.IsDir() {
			if file.Name() != "." && file.Name() != ".." {
				db.rwLocks[file.Name()] = new(sync.RWMutex)

				err := db.initTagsIndex(file.Name())
				if err != nil {
					return nil, err
				}
			}
		}
	}

	return db, nil
}

/*****************************************************************************/
// Public DB Methods
/*****************************************************************************/
/*---------- Find ----------*/
func (db *DB) Find(tblName string, rec Record, fileId string) error {
	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	err := db.loadRec(tblName, rec, fileId)
	if err != nil {
		return err
	}

	// Turn struct back into whatever it started out as in calling app.
	rec.Transform()

	return nil
}

/*---------- FindFirstIdForField ----------*/
func (db *DB) FindFirstIdForField(tblName string, searchField string, searchValue interface{}) (string, error) {
	var rec map[string]interface{}

	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	// For every file in the data dir until you find a match...
	for _, fileId := range db.fileIdsInDataDir(tblName) {
		filename := db.filePath(tblName, fileId)

		data, err := ioutil.ReadFile(filename)
		if err != nil {
			return "", err
		}

		err = json.Unmarshal(data, &rec)
		if err != nil {
			return "", err
		}

		switch searchValue.(type) {
		case int:
			if rec[searchField].(int) == searchValue.(int) {
				return fileId, nil
			}
		case int32:
			if rec[searchField].(int32) == searchValue.(int32) {
				return fileId, nil
			}
		case int64:
			if rec[searchField].(int64) == searchValue.(int64) {
				return fileId, nil
			}
		case string:
			if rec[searchField].(string) == searchValue.(string) {
				return fileId, nil
			}
		}
	}

	return "", nil
}

/*---------- FindIdsForField ----------*/
func (db *DB) FindIdsForField(tblName string, searchField string, searchValue interface{}) ([]string, error) {
	var rec map[string]interface{}
	var ids []string

	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	// For every file in the data dir...
	for _, fileId := range db.fileIdsInDataDir(tblName) {
		filename := db.filePath(tblName, fileId)

		data, err := ioutil.ReadFile(filename)
		if err != nil {
			return nil, err
		}

		err = json.Unmarshal(data, &rec)
		if err != nil {
			return nil, err
		}

		switch searchValue.(type) {
		case int:
			if rec[searchField].(int) == searchValue.(int) {
				ids = append(ids, fileId)
			}
		case string:
			if rec[searchField].(string) == searchValue.(string) {
				ids = append(ids, fileId)
			}
		}

	}

	return ids, nil
}

/*---------- FindIdsForTags ----------*/
func (db *DB) FindIdsForTags(tblName string, searchTags []string) ([]string, error) {
	var ids []string
	var possibleMatchingFileIdsMap map[string]int

	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	if len(searchTags) != 0 {
		// Need a map to hold possible file ids for answers whose tags include at least one of the search tags.
		possibleMatchingFileIdsMap = make(map[string]int)

		// For each one of the search tags...
		for _, tag := range searchTags {
			// If the search tag is in the index...
			if fileIds, ok := db.tagIndexes[tblName][tag]; ok {
				// Loop through all the file ids that have that tag in the index...
				for _, fileId := range fileIds {
					// If we have already added that file id to the map of possible matching file ids, then just add 1 to the number of
					// occurrences of that file id.
					if numOfOccurences, ok := possibleMatchingFileIdsMap[fileId]; ok {
						possibleMatchingFileIdsMap[fileId] = numOfOccurences + 1
						// Otherwise, add the file id as a new key in the map of possible matching file ids and set the number of occurrences to 1.
					} else {
						possibleMatchingFileIdsMap[fileId] = 1
					}
				}
			}
		}

		// How many search tags were entered?  We will use this number when we loop through all of the possible matches to determine if the
		// possible match has a number of occurrences as the number of search tags.  If it does, that means that that possible match had
		// all of the tags that we are searching for.
		searchTagsLen := len(searchTags)

		// Now, we only want the possible matching file ids that have a number of occurrences equal to the number of search tags.  If the
		// number of occurrences is less, that means that that particular answer did not have all of the search tags in it's tag list.
		for fileId, numOfOccurrences := range possibleMatchingFileIdsMap {
			if numOfOccurrences == searchTagsLen {
				ids = append(ids, fileId)
			}
		}
	}

	return ids, nil

}

/*---------- Create ----------*/
func (db *DB) Create(tblName string, rec interface{}) (string, error) {
	db.rwLocks[tblName].Lock()
	defer db.rwLocks[tblName].Unlock()

	fileId, err := db.nextAvailableFileId(tblName)
	if err != nil {
		return "", err
	}

	marshalledRec, err := json.Marshal(rec)

	if err != nil {
		return "", err
	}

	filename := db.filePath(tblName, fileId)

	err = ioutil.WriteFile(filename, marshalledRec, 0600)
	if err != nil {
		return "", err
	}

	err = db.initTagsIndex(tblName)
	if err != nil {
		return "", err
	}

	return fileId, nil
}

/*---------- Update ----------*/
func (db *DB) Update(tblName string, rec interface{}, fileId string) error {
	db.rwLocks[tblName].Lock()
	defer db.rwLocks[tblName].Unlock()

	// Is fileid valid?
	_, err := strconv.Atoi(fileId)
	if err != nil {
		return err
	}

	marshalledRec, err := json.Marshal(rec)

	if err != nil {
		return err
	}

	filename := db.filePath(tblName, fileId)

	err = ioutil.WriteFile(filename, marshalledRec, 0600)
	if err != nil {
		return err
	}

	db.initTagsIndex(tblName)
	if err != nil {
		return err
	}

	return nil
}

/*---------- Delete ----------*/
func (db *DB) Delete(tblName string, fileId string) error {
	_, err := strconv.Atoi(fileId)
	if err != nil {
		return err
	}

	filename := db.filePath(tblName, fileId)

	db.rwLocks[tblName].Lock()
	defer db.rwLocks[tblName].Unlock()

	err = os.Remove(filename)
	if err != nil {
		return err
	}

	err = db.initTagsIndex(tblName)
	if err != nil {
		return err
	}

	return nil
}

/*---------- Close ----------*/
func (db *DB) Close() {
	for _, rwLock := range db.rwLocks {
		rwLock.Lock()
		rwLock.Unlock()
	}
}

/*****************************************************************************/
// Private DB Methods
/*****************************************************************************/

/*---------- fileIdsInDataDir ----------*/
func (db *DB) fileIdsInDataDir(tblName string) []string {
	var ids []string

	files, _ := ioutil.ReadDir(db.tblPath(tblName))
	for _, file := range files {
		if !file.IsDir() {
			if path.Ext(file.Name()) == ".json" {
				ids = append(ids, file.Name()[:len(file.Name())-5])
			}
		}
	}

	return ids
}

/*---------- nextAvailableFileId ----------*/
func (db *DB) nextAvailableFileId(tblName string) (string, error) {
	var fileIds []int
	var nextFileId string

	for _, f := range db.fileIdsInDataDir(tblName) {
		fileId, err := strconv.Atoi(f)
		if err != nil {
			return "", err
		}

		fileIds = append(fileIds, fileId)
	}

	if len(fileIds) == 0 {
		nextFileId = "1"
	} else {
		sort.Ints(fileIds)
		lastFileId := fileIds[len(fileIds)-1]

		nextFileId = strconv.Itoa(lastFileId + 1)
	}

	return nextFileId, nil
}

/*---------- loadRec ----------*/
func (db *DB) loadRec(tblName string, rec interface{}, fileId string) error {
	filename := db.filePath(tblName, fileId)

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	err = json.Unmarshal(data, rec)

	return err
}

/*---------- tblPath ----------*/
func (db *DB) tblPath(tblName string) string {
	return path.Join(db.path, tblName)
}

/*---------- filePath ----------*/
func (db *DB) filePath(tblName string, fileId string) string {
	return fmt.Sprintf("%v/%v.json", db.tblPath(tblName), fileId)
}

/*---------- initTagsIndex ----------*/
func (db *DB) initTagsIndex(tblName string) error {
	var rec map[string]interface{}
	tagIndex := make(map[string][]string)

	// Delete all the entries in the index.
	for k := range db.tagIndexes {
		delete(db.tagIndexes[tblName], k)
	}

	// For every file in the data dir...
	for _, fileId := range db.fileIdsInDataDir(tblName) {
		filename := db.filePath(tblName, fileId)

		data, err := ioutil.ReadFile(filename)
		if err != nil {
			return err
		}

		err = json.Unmarshal(data, &rec)
		if err != nil {
			return err
		}

		// Convert back into a slice.
		tags := rec["tags"].([]interface{})

		// For every tag in the answer...
		for _, t := range tags {
			// Convert tag back into a string
			tag := t.(string)

			// If the tag already exists as a key in the index...
			if fileIds, ok := tagIndex[tag]; ok {
				// Add the file id to the list of ids for that tag, if it is not already in the list.
				if !stringInSlice(fileId, fileIds) {
					tagIndex[tag] = append(fileIds, fileId)
				}
			} else {
				// Otherwise, add the tag with associated new file id to the index.
				tagIndex[tag] = []string{fileId}
			}
		}

	}

	db.tagIndexes[tblName] = tagIndex

	return nil
}

//=============================================================================
// Helper Functions
//=============================================================================

/*---------- stringInSlice ----------*/
func stringInSlice(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}