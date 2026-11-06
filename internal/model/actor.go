package model

// SystemController is the Actor name for changes the controller makes.
const SystemController = "system:controller"

// SystemGateway is the Actor name for changes the gateway makes on its own,
// such as its token janitor ending app tokens their app no longer wants.
const SystemGateway = "system:gateway"

// SystemCLI is the Actor name for changes made with the javo command line.
const SystemCLI = "system:cli"

// Actor is who made a change, recorded on its audit row: a person (through a
// browser session, or through the personal API token whose id is Token), or a
// system component.
type Actor struct {
	// Person is the person's name; empty for a system actor.
	Person string
	// Token is the id of the API token the person used; empty for a session.
	Token string
	// System names a component (SystemController); empty for a person.
	System string
	// App is the object name of the coding agent whose token acted while
	// its owner trusted it (it acted without asking); empty otherwise.
	App string
}
