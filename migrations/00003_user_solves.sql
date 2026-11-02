-- +goose Up
CREATE TABLE user_solves (
 guild_id text NOT NULL REFERENCES guild_configs,
 user_id text NOT NULL CHECK (user_id ~ '^[1-9][0-9]{5,19}$'),
 problem_id uuid NOT NULL REFERENCES problems,
 publication_id uuid NOT NULL,
 solved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 solved_on date NOT NULL,
 source text NOT NULL CHECK (source IN ('button','backlog')),
 PRIMARY KEY (guild_id,user_id,problem_id),
 FOREIGN KEY (publication_id,guild_id,problem_id) REFERENCES problem_publications(id,guild_id,problem_id)
);
CREATE INDEX user_solves_day ON user_solves(guild_id,user_id,solved_on);
CREATE INDEX user_solves_period ON user_solves(guild_id,solved_on);

-- +goose Down
DROP TABLE user_solves;