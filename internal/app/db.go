package app

import (
	"context"
	"log"
	"os"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"go.uber.org/fx"
)

func ProvideDB(lc fx.Lifecycle) (*sqlx.DB, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}

	db, err := sqlx.Connect("postgres", dbURL)
	if err != nil {
		return nil, err
	}

	// Fx cerrará la DB automáticamente al apagar la app
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			log.Println("Cerrando conexión a la base de datos...")
			return db.Close()
		},
	})

	return db, nil
}
