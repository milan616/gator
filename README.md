# gator
boot.dev Gator: blog aggregator

Project Requirements:
  - Go
  - Postgres

Install:
  - Run "go install"
  - Create ".gatorconfig.json" in user home directory with following keys:
    - db_url: your Postgres database URL
    - current_user_name: <blank>

Usage:
  - gator <command>
  - Basic commands: login, register, users, agg, feeds
  - Login required commands: addfeed, follow, following, unfollow, browse
