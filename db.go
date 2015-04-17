// Package ivy provides a simple, file-based Database Management System (DBMS)
// that can be used in Go programs.
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

// Type Record is an interface that your table model needs to implement.
// The AfterFind method is a callback that will run inside the Find method,
// right after the record is found and populated. This method will be passed the
// database connection and the record id of the record just found. In your
// implementation of this method, you should convert the record interface back
// to it's original type. Take a look at example.go in the examples directory
// for a look at how to do this.
type Record interface {
	AfterFind(*DB, string)
}

// Type DB is a struct representing the database connection.
type DB struct {
	path          string
	rwLocks       map[string]*sync.RWMutex
	fieldsToIndex map[string][]string
	tagIndexes    map[string]map[string][]string
	fldIndexes    map[string]map[string]map[string][]string
}

// OpenDB initializes an ivy database.
// It returns a pointer to a DB struct and any error encountered.
func OpenDB(dbPath string, fieldsToIndex map[string][]string) (*DB, error) {
	db := new(DB)
	db.path = dbPath
	db.fieldsToIndex = fieldsToIndex

	err := db.performChecks()
	if err != nil {
		return nil, err
	}

	db.rwLocks = make(map[string]*sync.RWMutex)

	db.tagIndexes = make(map[string]map[string][]string)
	db.fldIndexes = make(map[string]map[string]map[string][]string)

	files, _ := ioutil.ReadDir(db.path)

	for _, file := range files {
		if file.IsDir() {
			if file.Name() != "." && file.Name() != ".." {
				db.rwLocks[file.Name()] = new(sync.RWMutex)
			}
		}
	}

	for tblName := range db.fieldsToIndex {
		err := db.initTblIndexes(tblName)
		if err != nil {
			return nil, err
		}
	}

	return db, nil
}

//*****************************************************************************
// Public DB Methods
//*****************************************************************************

// Find loads up a Record struct with the record corresponding to a supplied id.
// It takes a table name, a pointer to a Record struct, and an id specifying the
// record to find. It populates the Record struct attributes with values from
// the found record. It returns any error encountered.
func (db *DB) Find(tblName string, rec Record, fileId string) error {
	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	err := db.loadRec(tblName, rec, fileId)
	if err != nil {
		return err
	}

	rec.AfterFind(db, fileId)

	return nil
}

// FindAllIds return all ids for the specified table name.
// It takes a table name.
// It returns a slice of ids and any error encountered.
func (db *DB) FindAllIds(tblName string) ([]string, error) {
	var ids []string

	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	// For every file in the data dir...
	for _, fileId := range db.fileIdsInDataDir(tblName) {
		ids = append(ids, fileId)
	}

	return ids, nil
}

// FindFirstIdForField returns the first record id that matches the supplied
// search criteria. It takes a table name, a field name to search on, and a
// value to search for. It returns a record id and any error encountered.
func (db *DB) FindFirstIdForField(tblName string, searchField string, searchValue string) (string, error) {
	results, err := db.FindAllIdsForField(tblName, searchField, searchValue)
	if err != nil {
		return "", err
	}

	return results[0], nil
}

// FindAllIdsForField returns all record ids that match the supplied search
// criteria.  It takes a table name, a field name to search on, and a value
// to search for.  It returns a slice of record ids and any error encountered.
func (db *DB) FindAllIdsForField(tblName string, searchField string, searchValue string) ([]string, error) {
	var rec map[string]interface{}
	var ids []string

	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	// If we have an index on that field...
	if ids, ok := db.fldIndexes[tblName][searchField][searchValue]; ok {
		return ids, nil
	}

	// Otherwise, for every file in the data dir...
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

		if rec[searchField].(string) == searchValue {
			ids = append(ids, fileId)
		}
	}

	return ids, nil
}

// FindAllIdsForTag returns all record ids that match the all of the supplied
// search tags. It takes a table name, and a slice of tags to search for.
// It returns a slice of record ids and any error encountered.
func (db *DB) FindAllIdsForTags(tblName string, searchTags []string) ([]string, error) {
	var ids []string
	var possibleMatchingFileIdsMap map[string]int

	db.rwLocks[tblName].RLock()
	defer db.rwLocks[tblName].RUnlock()

	if len(searchTags) != 0 {
		// Need a map to hold possible file ids for answers whose tags include at
		// least one of the search tags.
		possibleMatchingFileIdsMap = make(map[string]int)

		// For each one of the search tags...
		for _, tag := range searchTags {
			// If the search tag is in the index...
			if fileIds, ok := db.tagIndexes[tblName][tag]; ok {
				// Loop through all the file ids that have that tag in the index...
				for _, fileId := range fileIds {
					// If we have already added that file id to the map of possible
					// matching file ids, then just add 1 to the number of occurrences of
					// that file id.
					if numOfOccurences, ok := possibleMatchingFileIdsMap[fileId]; ok {
						possibleMatchingFileIdsMap[fileId] = numOfOccurences + 1
						// Otherwise, add the file id as a new key in the map of possible
						// matching file ids and set the number of occurrences to 1.
					} else {
						possibleMatchingFileIdsMap[fileId] = 1
					}
				}
			}
		}

		// How many search tags were entered?  We will use this number when we loop
		// through all of the possible matches to determine if the possible match
		// has the same number of occurrences as the number of search tags.  If it
		// does, that means that that possible match had all of the tags that we are
		// searching for.
		searchTagsLen := len(searchTags)

		// Now, we only want the possible matching file ids that have a number of
		// occurrences equal to the number of search tags.  If the number of
		// occurrences is less, that means that that particular answer did not
		// have all of the search tags in it's tag list.
		for fileId, numOfOccurrences := range possibleMatchingFileIdsMap {
			if numOfOccurrences == searchTagsLen {
				ids = append(ids, fileId)
			}
		}
	}

	return ids, nil

}

// Create creates a new record for the specified table.
// It takes a table name, and a struct representing the record data.
// It returns the id of the newly created record and any error encountered.
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

	err = db.initTblIndexes(tblName)
	if err != nil {
		return fileId, err
	}

	return fileId, nil
}

// Update updates a record for the specified table.
// It takes a table name, a struct representing the record data, and the record
// id of the record to be changed.  It returns any error encountered.
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

	err = db.initTblIndexes(tblName)
	if err != nil {
		return err
	}

	return nil
}

// Delete deletes a record for the specified table.
// It takes a table name and the record id of the record to be deleted..
// It returns any error encountered.
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

	err = db.initTblIndexes(tblName)
	if err != nil {
		return err
	}

	return nil
}

// Close closes an ivy database.
func (db *DB) Close() {
	for _, rwLock := range db.rwLocks {
		rwLock.Lock()
		rwLock.Unlock()
	}
}

//*****************************************************************************
// Private DB Methods
//*****************************************************************************

// fileIdsInDataDir returns all file ids in a directory.
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

// filePath returns a file name for a table name and a file id.
func (db *DB) filePath(tblName string, fileId string) string {
	return fmt.Sprintf("%v/%v.json", db.tblPath(tblName), fileId)
}

// loadRec reads a json file into the supplied interface.
func (db *DB) loadRec(tblName string, rec interface{}, fileId string) error {
	filename := db.filePath(tblName, fileId)

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	err = json.Unmarshal(data, rec)

	return err
}

// initNonTagsIndexes initializes all non-tag indexes for a table.
func (db *DB) initNonTagsIndexes(tblName string) error {
	var rec map[string]interface{}

	// Delete all the indexes for this table.
	for k := range db.fldIndexes[tblName] {
		delete(db.fldIndexes[tblName], k)
	}

	db.fldIndexes[tblName] = make(map[string]map[string][]string)

	// Reinit all the indexes for this table.
	for _, fldName := range db.fieldsToIndex[tblName] {
		if fldName != "tags" {
			db.fldIndexes[tblName][fldName] = make(map[string][]string)
		}
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

		for _, fldName := range db.fieldsToIndex[tblName] {
			// Skip tags because we index them separately
			if fldName == "tags" {
				continue
			}

			// Convert back into a string.
			fldValue := rec[fldName].(string)

			// If the field value already exists as a key in the index...
			if fileIds, ok := db.fldIndexes[tblName][fldName][fldValue]; ok {
				// Add the file id to the list of ids for that field value, if it is not
				// already in the list.
				if !stringInSlice(fileId, fileIds) {
					db.fldIndexes[tblName][fldName][fldValue] = append(fileIds, fileId)
				}
			} else {
				// Otherwise, add the field value with associated new file id to the
				// index.
				db.fldIndexes[tblName][fldName][fldValue] = []string{fileId}
			}
		}
	}

	return nil
}

// initTagsIndex initializes all tag indexes for a database.
func (db *DB) initTagsIndex(tblName string) error {
	var rec map[string]interface{}
	tagIndex := make(map[string][]string)

	// Delete all the entries in the index.
	for k := range db.tagIndexes[tblName] {
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
				// Add the file id to the list of ids for that tag, if it is not already
				// in the list.
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

// initTblIndexes initializes all indexes for a table.
func (db *DB) initTblIndexes(tblName string) error {
	if fldNames, ok := db.fieldsToIndex[tblName]; ok {
		err := db.initNonTagsIndexes(tblName)
		if err != nil {
			return err
		}

		if stringInSlice("tags", fldNames) {
			db.initTagsIndex(tblName)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// nextAvailableFileId returns the next ascending available file id in a
// directory.
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

// performChecks does validation checks on a database config.
func (db *DB) performChecks() error {
	if _, err := os.Stat(db.path); os.IsNotExist(err) {
		return err
	}
	for tbl := range db.fieldsToIndex {
		if _, err := os.Stat(db.tblPath(tbl)); os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

// tblPath returns the file path for a table directory.
func (db *DB) tblPath(tblName string) string {
	return path.Join(db.path, tblName)
}

//=============================================================================
// Helper Functions
//=============================================================================

// stringInSlice answers whether a string exists in a slice.
func stringInSlice(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
