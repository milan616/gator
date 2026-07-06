package main

import (
	"errors"
	"fmt"
	"time"
	"context"
	"database/sql"
	"net/http"
	"io"
	"encoding/xml"
	"html"
	"log"
	"strings"
	"strconv"

	"github.com/milan616/gator/internal/config"
	"github.com/milan616/gator/internal/database"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// state holds a pointer to our application configuration
type state struct {
	db	*database.Queries
	cfg *config.Config
}

// command represents a single instruction given by the user
type command struct {
	name string
	args []string
}

// commands holds the registry of available handlers
type commands struct {
	handlers map[string]func(*state, command) error
}

type RSSFeed struct {
	Channel struct {
		Title       string    `xml:"title"`
		Link        string    `xml:"link"`
		Description string    `xml:"description"`
		Item        []RSSItem `xml:"item"`
	} `xml:"channel"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// register registers a new handler function for a command name
func (c *commands) register(name string, f func(*state, command) error) {
	c.handlers[name] = f
}

// run executes a registered command if it exists
func (c *commands) run(s *state, cmd command) error {
	handler, exists := c.handlers[cmd.name]
	if !exists {
		return fmt.Errorf("unknown command: %s", cmd.name)
	}
	return handler(s, cmd)
}

func middlewareLoggedIn(handler func(s *state, cmd command, user database.User) error) func(*state, command) error {
	return func(s *state, cmd command) error {
		ctx := context.Background()
		
		// Fetch the current user once, right here in the middleware wrapper
		user, err := s.db.GetUser(ctx, s.cfg.CurrentUserName)
		if err != nil {
			return fmt.Errorf("current user '%s' not found: %w", s.cfg.CurrentUserName, err)
		}

		// Pass the fetched user directly to the wrapped inner handler
		return handler(s, cmd, user)
	}
}

// handlerLogin sets the current user in the config file
func handlerLogin(s *state, cmd command) error {
	if len(cmd.args) == 0 {
		return errors.New("the login handler expects a single argument, the username")
	}

	username := cmd.args[0]
	ctx := context.Background()
	user, err := s.db.GetUser(ctx, username)
	if err != nil {
		return fmt.Errorf("username '%s' does not exist", username)
	}
	
	err = s.cfg.SetUser(user.Name)
	if err != nil {
		return fmt.Errorf("could not set user: %w", err)
	}

	fmt.Printf("User has been set to: %s\n", username)
	return nil
}

func handlerRegister(s *state, cmd command) error {
	if len(cmd.args) == 0 {
		return errors.New("the register handler expects a single argument, the username")
	}

	username := cmd.args[0]
	ctx := context.Background()

	// Build the params matching the updated sqlc struct
	params := database.CreateUserParams{
		ID:        uuid.New(), // Generates a fresh RFC 4122 UUID
		CreatedAt: sql.NullTime{Time: time.Now(), Valid: true},
		UpdatedAt: sql.NullTime{Time: time.Now(), Valid: true},
		Name:      username,
	}

	// Call the database to create the user record
	user, err := s.db.CreateUser(ctx, params)
	if err != nil {
		return fmt.Errorf("could not create user: %w", err)
	}

	// Set this new user as the current user in our configuration file
	err = s.cfg.SetUser(user.Name)
	if err != nil {
		return fmt.Errorf("user created, but failed to update configuration: %w", err)
	}

	// Print confirmation and details as expected by the lesson
	fmt.Printf("User was successfully created: %s\n", user.Name)
	fmt.Println("User Details:")
	fmt.Printf("  ID:         %v\n", user.ID)
	fmt.Printf("  Created At: %v\n", user.CreatedAt.Time)
	fmt.Printf("  Updated At: %v\n", user.UpdatedAt.Time)
	
	return nil
}

func handlerReset(s *state, cmd command) error {
	ctx := context.Background()

	err := s.db.Reset(ctx)
	if err != nil {
		return fmt.Errorf("failed to reset users table: %w", err)
	}

	return nil
}

func handlerUsers(s *state, cmd command) error {
	ctx := context.Background()

	users, err := s.db.GetUsers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list users table: %w", err)
	}

	for _, user := range users {
		if user.Name == s.cfg.CurrentUserName {
			fmt.Printf("%s (current)\n", user.Name)
		} else {
			fmt.Printf("%s\n", user.Name)
		}
	}

	return nil
}

func fetchFeed(ctx context.Context, feedURL string) (*RSSFeed, error) {
	// 1. Create the request tied to the context
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 2. Add the User-Agent header as required
	req.Header.Set("User-Agent", "gator")

	// 3. Execute the HTTP request using the default client
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Check status code sanity
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bad HTTP status code: %d", resp.StatusCode)
	}

	// 4. Read all response body bytes
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// 5. Unmarshal XML into a fresh RSSFeed struct
	var feed RSSFeed
	err = xml.Unmarshal(data, &feed)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal XML: %w", err)
	}

	// 6. Decode escaped HTML entities for Channel Title and Description
	feed.Channel.Title = html.UnescapeString(feed.Channel.Title)
	feed.Channel.Description = html.UnescapeString(feed.Channel.Description)

	// Sweep through every single individual Item to clean up their strings too
	for i := range feed.Channel.Item {
		feed.Channel.Item[i].Title = html.UnescapeString(feed.Channel.Item[i].Title)
		feed.Channel.Item[i].Description = html.UnescapeString(feed.Channel.Item[i].Description)
	}

	return &feed, nil
}

func scrapeFeeds(s *state) error {
	ctx := context.Background()

	// 1. Get the next oldest feed that needs fetching
	feed, err := s.db.GetNextFeedToFetch(ctx)
	if err != nil {
		return fmt.Errorf("could not look up next feed: %w", err)
	}

	// 2. Instantly mark it as fetched so it shifts to the back of the line
	_, err = s.db.MarkFeedFetched(ctx, feed.ID)
	if err != nil {
		return fmt.Errorf("failed to mark feed %s as fetched: %w", feed.Name, err)
	}

	// 3. Fetch and parse the live XML feed over the network
	fmt.Printf("Fetching feed: %s (%s)...\n", feed.Name, feed.Url)
	rssFeed, err := fetchFeed(ctx, feed.Url)
	if err != nil {
		return fmt.Errorf("failed parsing feed %s: %w", feed.Name, err)
	}

	// 4. Iterate over feed items and attempt to persist each post
	for _, item := range rssFeed.Channel.Item {
		// Attempt parsing the publication time accurately
		var pubTime time.Time
		var parsed bool

		// Check common RSS feed time specifications
		layouts := []string{time.RFC1123, time.RFC1123Z}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, item.PubDate); err == nil {
				pubTime = t
				parsed = true
				break
			}
		}

		// Fallback layout if the time format deviates slightly from standards
		if !parsed {
			pubTime = time.Now()
		}

		// Map string descriptions to nullable equivalents
		description := sql.NullString{
			String: item.Description,
			Valid:  item.Description != "",
		}

		_, err = s.db.CreatePost(ctx, database.CreatePostParams{
			ID:          uuid.New(),
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
			Title:       sql.NullString{String: item.Title, Valid: item.Title != ""},
			Url:         sql.NullString{String: item.Link, Valid: item.Link != ""},
			Description: description,
			PublishedAt: sql.NullTime{Time: pubTime, Valid: true},
			FeedID:      feed.ID,
		})

		if err != nil {
			// Catch string/database constraint collisions safely
			if pgErr, ok := err.(*pq.Error); ok && pgErr.Code == "23505" {
				// 23505 is the PostgreSQL code for a unique_violation (URL already exists)
				// Silently skip as requested by the assignment
				continue
			}
			// Log any other non-duplicate constraint errors cleanly
			log.Printf("Error saving post '%s': %v", item.Title, err)
		}
	}

	return nil
}

func handlerAgg(s *state, cmd command) error {
	if len(cmd.args) < 1 {
		return errors.New("agg requires 1 argument: time_between_reqs (e.g. 1s, 1m, 1h)")
	}

	// Parse duration string into a concrete time.Duration value
	timeBetweenRequests, err := time.ParseDuration(cmd.args[0])
	if err != nil {
		return fmt.Errorf("invalid duration format: %w", err)
	}

	fmt.Printf("Collecting feeds every %s\n", timeBetweenRequests)

	// Custom immediate-execution loop structure using a Go ticker
	ticker := time.NewTicker(timeBetweenRequests)
	defer ticker.Stop()

	// The initial semi-colon executes scrapeFeeds immediately before blocking on the channel tick
	for ; ; <-ticker.C {
		err := scrapeFeeds(s)
		if err != nil {
			fmt.Printf("Scraper error: %v\n", err)
			// We don't return the error here because we want the loop 
			// to keep running even if one network request fails!
		}
	}
}

func handlerListFeeds(s *state, cmd command) error {
	ctx := context.Background()

	feeds, err := s.db.ListFeeds(ctx)
	if err != nil {
		return fmt.Errorf("failed to list feeds table: %w", err)
	}

	for _, feed := range feeds {
		fmt.Printf("Name: %s\n", feed.Name)
		fmt.Printf("URL: %s\n", feed.Url)
		fmt.Printf("Username: %s\n", feed.Username)
	}

	return nil
}

func handlerAddFeed(s *state, cmd command, user database.User) error {
	if len(cmd.args) != 2 {
		return errors.New("addfeed requires 2 args: name, url")
	}

	name := cmd.args[0]
	url := cmd.args[1]
	ctx := context.Background()

	feedParams := database.CreateFeedParams{
		ID:        uuid.New(),
		CreatedAt: sql.NullTime{Time: time.Now(), Valid: true},
		UpdatedAt: sql.NullTime{Time: time.Now(), Valid: true},
		Name:      name,
		Url:       url,
		UserID:    user.ID, // Already fetched by middleware!
	}

	feed, err := s.db.CreateFeed(ctx, feedParams)
	if err != nil {
		return fmt.Errorf("could not create feed: %w", err)
	}

	followParams := database.CreateFeedFollowParams{
		ID:        uuid.New(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		UserID:    user.ID,
		FeedID:    feed.ID,
	}

	_, err = s.db.CreateFeedFollow(ctx, followParams)
	if err != nil {
		return fmt.Errorf("feed created, but failed to automatically follow it: %w", err)
	}

	fmt.Printf("Feed was successfully created: %s\n", feed.Name)
	fmt.Println("Feed Details:")
	fmt.Printf("  ID:          %v\n", feed.ID)
	fmt.Printf("  Created At:  %v\n", feed.CreatedAt.Time)
	fmt.Printf("  Updated At:  %v\n", feed.UpdatedAt.Time)
	fmt.Printf("  Name:        %v\n", feed.Name)
	fmt.Printf("  URL:         %v\n", feed.Url)
	fmt.Printf("  User ID:     %v\n", feed.UserID)

	return nil
}

func handlerFollow(s *state, cmd command, user database.User) error {
	if len(cmd.args) != 1 {
		return errors.New("follow requires 1 arg: url")
	}
	feedURL := cmd.args[0]
	ctx := context.Background()

	feed, err := s.db.GetFeedByUrl(ctx, feedURL)
	if err != nil {
		return fmt.Errorf("feed with URL '%s' not found: %w", feedURL, err)
	}

	params := database.CreateFeedFollowParams{
		ID:        uuid.New(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		UserID:    user.ID, // Already fetched by middleware!
		FeedID:    feed.ID,
	}

	followRow, err := s.db.CreateFeedFollow(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to follow feed: %w", err)
	}

	fmt.Printf("Successfully followed feed!\n")
	fmt.Printf("User: %s\n", followRow.UserName)
	fmt.Printf("Feed: %s\n", followRow.FeedName)

	return nil
}

func handlerFollowing(s *state, cmd command, user database.User) error {
	ctx := context.Background()

	follows, err := s.db.GetFeedFollowsForUser(ctx, user.ID) // Already fetched by middleware!
	if err != nil {
		return fmt.Errorf("failed to get feed follows: %w", err)
	}

	if len(follows) == 0 {
		fmt.Printf("%s is not following any feeds yet.\n", user.Name)
		return nil
	}

	fmt.Printf("Feeds followed by %s:\n", user.Name)
	for _, follow := range follows {
		fmt.Printf(" * %s\n", follow.FeedName)
	}

	return nil
}

func handlerUnfollow(s *state, cmd command, user database.User) error {
	if len(cmd.args) != 1 {
		return errors.New("follow requires 1 arg: url")
	}
	feedURL := cmd.args[0]
	ctx := context.Background()

	feed, err := s.db.GetFeedByUrl(ctx, feedURL)
	if err != nil {
		return fmt.Errorf("feed with URL '%s' not found: %w", feedURL, err)
	}

	params := database.UnfollowFeedByUserAndIDParams {
		UserID:		user.ID,
		FeedID:		feed.ID,
	}

	err = s.db.UnfollowFeedByUserAndID(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to unfollow feed: %w", err)
	}

	fmt.Printf("%s unfollowed %s", user.Name, feed.Name)

	return nil
}

func handlerBrowse(s *state, cmd command, user database.User) error {
	limit := 2
	
	if len(cmd.args) > 0 {
		num, err := strconv.Atoi(cmd.args[0])
		if err != nil {
			return fmt.Errorf("limit was not a number")
		}
		limit = num
	}

	params := database.GetPostsForUserParams {
		UserID:		user.ID,
		Limit:		int32(limit),
	}

	ctx := context.Background()
	posts, err := s.db.GetPostsForUser(ctx, params)
	if err != nil {
		return fmt.Errorf("Could not retrieve posts: %w", err)
	}

	for _, post := range posts {
	    // Check if Title is valid before printing
	    title := "Untitled"
	    if post.Title.Valid {
	        title = post.Title.String
	    }

	    // Format the time cleanly if it's valid
	    dateStr := "Unknown Date"
	    if post.PublishedAt.Valid {
	        // You can format it nicely using Go's reference time layout
	        dateStr = post.PublishedAt.Time.Format("2006-01-02 15:04:05")
	    }

	    description := ""
	    if post.Description.Valid {
	        description = post.Description.String
	    }

	    fmt.Printf("Title: %s\n", title)
	    fmt.Printf("Date:  %s\n", dateStr)
	    fmt.Printf("Post:\n%s\n", description)
	    fmt.Println(strings.Repeat("-", 40)) // Visual separator between browse entries
	}

	return nil
}
