package ammo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	. "github.com/adamdevigili/tarkov-charts-models"
	"github.com/kelseyhightower/envconfig"
	"github.com/mitchellh/mapstructure"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func AmmoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		GetAmmo(w, r)
	} else if r.Method == http.MethodPut {
		UpdateAmmo(w, r)
	}
}

func GetAmmo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var config Config
	err := envconfig.Process("", &config)
	if err != nil {
		log.Fatal(err.Error())
	}

	if config.VERCEL_ENV != "development" {
		if r.Header.Get(APIKeyHeader) != config.TC_API_KEY {
			log.Printf("incoming request API key invalid: %s", r.Header.Get(APIKeyHeader))
			fmt.Fprint(w, "not authorized")

			return
		}
	} else {
		log.Printf("%+v\n", config)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// This is the map we will build of all ammo and relevant information throughout this function
	// We will eventually write this to our data store
	// parsedAmmo := map[string]*Ammo{}
	// ammoByCaliber := map[string]map[string]*Ammo{}

	clientOptions := options.Client().ApplyURI(fmt.Sprintf(
		"mongodb+srv://%s:%s@%s/%s?retryWrites=true&w=majority",
		config.MONGO_USER,
		config.MONGO_PASSWORD,
		config.MONGO_CLUSTER_PATH,
		config.MONGO_DB_NAME,
	))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mongoClient, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal(err)
	}

	defer mongoClient.Disconnect(ctx)

	// ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second); defer cancel()

	if err = mongoClient.Ping(ctx, readpref.Primary()); err != nil {
		log.Fatal(err)
	}

	log.Print("successfully connected to database")

	items := mongoClient.Database(config.MONGO_DB_NAME).Collection("ammo")

	// parsedBSON, err := bson.Marshal(ammoByCaliber)
	// if err != nil {
	// 	log.Fatal("error marshalling BSON", err)
	// }

	log.Print("attempting to read from database")

	var ammo bson.M
	err = items.FindOne(
		ctx,
		bson.M{"_name": "ammo_data"},
	).Decode(&ammo)
	if err != nil {
		log.Fatal("error fetching from database", err)
	}

	log.Printf("successfully fetched data")

	w.WriteHeader(http.StatusOK)

	// Cache response in CDN for 30 minutes
	// w.Header().Set("Cache-Control", "s-maxage=1800")

	json.NewEncoder(w).Encode(ammo)
	// jsonString, _ := json.Marshal(ammo["data"])
	// fmt.Fprint(w, string(jsonString))
}

func UpdateAmmo(w http.ResponseWriter, r *http.Request) {
	var config Config
	err := envconfig.Process("", &config)
	if err != nil {
		log.Fatal(err.Error())
	}

	if config.VERCEL_ENV == "development" {
		log.Printf("%+v\n", config)
	}

	if config.VERCEL_ENV != "development" {
		if r.Header.Get(APIKeyHeader) != config.TC_API_KEY {
			log.Printf("incoming request API key invalid: %s", r.Header.Get(APIKeyHeader))
			fmt.Fprint(w, "not authorized")

			return
		}
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// This is the map we will build of all ammo and relevant information throughout this function
	// We will eventually write this to our data store
	// parsedAmmo := map[string]*Ammo{}
	ammoByCaliber := map[string]map[string]*Ammo{}

	client := &http.Client{Timeout: time.Second * 10}

	// Build GraphQL query to fetch only ammo items
	query, _ := json.Marshal(map[string]string{
		"query": `
			{
				itemsByType(type: ammo) {
					id
					name
					shortName
					iconLink
					avg24hPrice
				}
			}
        `,
	})

	// Fetch all ammo IDs, as well as names, short names, icon links, and average 24hr price
	request, _ := http.NewRequest("POST", "https://tarkov-tools.com/graphql", bytes.NewBuffer(query))
	response, err := client.Do(request)
	if err != nil {
		log.Printf("GraphQL request failed: %s\n", err)
	} else {
		log.Println("successfully fetched ammo IDs")
	}
	defer response.Body.Close()

	data, _ := ioutil.ReadAll(response.Body)
	graphQLResp := &GraphQLResponse{}
	json.Unmarshal(data, graphQLResp)

	// Fetch current pen/damage data from BSG API

	request, _ = http.NewRequest(http.MethodGet, "https://raw.githack.com/TarkovTracker/tarkovdata/master/ammunition.json", nil)
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		log.Fatalf("failed to fetch pen/damage data. Code: %d", response.StatusCode)
	} else {
		log.Println("successfully fetched pen/damage data")
	}

	data, _ = ioutil.ReadAll(response.Body)

	var f interface{}
	err = json.Unmarshal(data, &f)
	if err != nil {
		log.Fatal("error parsing JSON: ", err)
	}

	// Need to do some Go magic to consume the TarkovTracker JSON properly. Also leverage the mapstructure package
	itemsMap := f.(map[string]interface{})
	var result TarkovTrackerAmmo
	for _, item := range graphQLResp.Data.ItemsByType {
		err = mapstructure.Decode(itemsMap[item.ID], &result)
		if err != nil {
			log.Print("mapstructure error: ", err, item)
		}

		// When querying for ammo types, we currently get grenades and ammo boxes. Ignore them
		if !strings.Contains(result.Name, "grenade") &&
			!strings.Contains(result.Name, "pack") &&
			!strings.Contains(result.Caliber, "Caliber40x46") {
			// Initialize the final map with BSG data
			// parsedAmmo[item.ID] = &Ammo{
			// 	Caliber:     result.Caliber,
			// 	Name:        result.ShortName,
			// 	Damage:      result.Ballistics.Damage,
			// 	Penetration: result.Ballistics.PenetrationPower,
			// 	Price: item.Avg24hPrice,
			// }

			if ammoByCaliber[result.Caliber] == nil {
				ammoByCaliber[result.Caliber] = make(map[string]*Ammo)
			}
			ammoByCaliber[result.Caliber][item.ID] = &Ammo{
				Caliber:     result.Caliber,
				Name:        result.Name,
				ShortName:   result.ShortName,
				Damage:      result.Ballistics.Damage,
				Penetration: result.Ballistics.PenetrationPower,
				Price:       item.Avg24hPrice,
			}
		}
	}

	/* tarkov-market integration deprecated
	// Fetch current prices of all items and parse.
	// Other option would be to fetch all 100+ ammo types individually, no thanks.
	// Also no option to fetch only ammo items via this API :(

	// NOTE: All the API requests can be done in parallel, however since this is intended to be run
	// periodically (so performance isn't that important), and as a lambda function (where memory
	// usage is important), we run these in sequence
	request, _ = http.NewRequest(http.MethodGet, "https://tarkov-market.com/api/v1/items/all", nil)
	request.Header.Set("x-api-key", config.TM_API_KEY)
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		log.Fatalf("failed to fetch ammo prices. Code: %d", response.StatusCode)
	} else {
		log.Println("succesfully fetched ammo prices")
	}
	data, _ = ioutil.ReadAll(response.Body)

	var fleaMarketData TarkovMarketItems
	err = json.Unmarshal(data, &fleaMarketData)
	if err != nil {
		log.Fatal("error parsing JSON: ", err)
	}

	// Since we have all items in Tarkov returned here, and no O(1) access by ID,
	// iterate over all entries, and update the relevant fields in our target map with
	// the average 24hr price
	for _, item := range fleaMarketData {
		if _, found := parsedAmmo[item.BsgID]; found {
			parsedAmmo[item.BsgID].Price = item.Avg24HPrice

			for _, ammoMap := range ammoByCaliber {
				if _, found := ammoMap[item.BsgID]; found {
					ammoMap[item.BsgID].Price = item.Avg24HPrice
				}
			}
		}
	}

	*/

	/* JSONBin integration deprecated. Leaving for later reference or...something

	Post the resulting JSON to jsonbin.io. We will probably want to store this in a more
	mature data store (DynamoDB) in the future, but for now this is a good tool for rapid
	development

	binID := config.JSONBIN_BIN_ID
	binAPIKEY := config.JSONBIN_API_KEY
	binURL := fmt.Sprintf("https://api.jsonbin.io/v3/b/%s", binID)

	req, _ := http.NewRequest(http.MethodPut, binURL, bytes.NewBuffer(parsedJSON))
	req.Header.Set("X-Master-Key", binAPIKEY)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil || response.StatusCode != http.StatusOK {
		log.Fatalf("failed to write to the data store. Code: %d", response.StatusCode)
	} else {
		log.Println("succesfully wrote to the data store")
	}
	defer resp.Body.Close()

	*/

	clientOptions := options.Client().ApplyURI(fmt.Sprintf(
		"mongodb+srv://%s:%s@%s/%s?retryWrites=true&w=majority",
		config.MONGO_USER,
		config.MONGO_PASSWORD,
		config.MONGO_CLUSTER_PATH,
		config.MONGO_DB_NAME,
	))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mongoClient, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer mongoClient.Disconnect(ctx)

	if err = mongoClient.Ping(ctx, readpref.Primary()); err != nil {
		log.Fatal(err)
	}

	log.Print("successfully connected to database")

	items := mongoClient.Database(config.MONGO_DB_NAME).Collection("ammo")

	log.Print("attempting to write updated data to database")

	res, err := items.ReplaceOne(
		ctx,
		bson.M{"_name": "ammo_data"},
		bson.D{
			{"_name", "ammo_data"},
			{"_updated_at", time.Now().Format(time.RFC822)},
			{"data", ammoByCaliber}})
	if err != nil {
		log.Fatal("error writing to database", err)
	}

	log.Printf("successfully updated ammo data, number modified: %d", res.ModifiedCount)

	fmt.Fprint(w, "success")
}
