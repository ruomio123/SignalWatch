package agent

import (
	"fmt"
	"signalwatch/internal/generation"
)

type paperStageValidation struct {
	Evidence  []Citation
	Questions []PaperQuestion
	Claims    []reviewClaim
}

// Stage semantics are explicit. QA and report analysis have different wire
// contracts, and a repair must always promote its own original analysis stage.
type paperStageContract struct {
	Kind          string
	Field         string
	Tokens        int
	Schema        *generation.Schema
	RepairStage   string
	OriginalStage string
	Validate      func([]byte, paperStageValidation) error
}

var paperStageContracts = func() map[string]paperStageContract {
	stages := map[string]paperStageContract{}
	for _, field := range paperFields {
		contract := paperStageContract{Kind: "report", Field: field, Tokens: 8192, Schema: paperSchemasFor(field).full, RepairStage: "repairing_" + field,
			Validate: func(raw []byte, input paperStageValidation) error {
				_, err := decodeFieldFor(raw, input.Evidence, field, true)
				return err
			}}
		stages["analyzing_"+field] = contract
		contract.OriginalStage = "analyzing_" + field
		contract.RepairStage = ""
		stages["repairing_"+field] = contract
	}
	answer := paperStageContract{Kind: "answer", Field: "answer", Tokens: 8192, Schema: paperAnswerSchemas.full, RepairStage: "repairing_answer",
		Validate: func(raw []byte, input paperStageValidation) error {
			_, err := decodePaperAnswer(raw, input.Evidence, input.Questions)
			return err
		}}
	stages["analyzing_answer"] = answer
	supplement := answer
	supplement.Schema = paperSupplementAnswerSchemas.full
	supplement.RepairStage = "repairing_answer_supplement"
	supplement.Validate = func(raw []byte, input paperStageValidation) error {
		_, err := decodePaperSupplementAnswer(raw, input.Evidence, input.Questions)
		return err
	}
	stages[paperSupplementStage] = supplement
	supplement.OriginalStage, supplement.RepairStage = paperSupplementStage, ""
	stages["repairing_answer_supplement"] = supplement
	answer.RepairStage = ""
	answer.OriginalStage = "analyzing_answer"
	stages["repairing_answer"] = answer
	stages["normalizing_question"] = paperStageContract{Kind: "question", Tokens: 4096, Schema: paperQuestionSchema,
		Validate: func(raw []byte, _ paperStageValidation) error { _, err := decodePaperQuestions(raw); return err }}
	for i := 1; i <= 18; i++ {
		stages[fmt.Sprintf("extracting_batch_%d", i)] = paperStageContract{Kind: "extraction", Tokens: 4096, Schema: batchSchema,
			Validate: func(raw []byte, input paperStageValidation) error {
				_, err := decodeBatch(raw, input.Evidence)
				return err
			}}
	}
	for i := 0; i < 6; i++ {
		stages[paperReviewStage(i)] = paperStageContract{Kind: "review", Tokens: 4096, Schema: reviewSchema,
			Validate: func(raw []byte, input paperStageValidation) error {
				_, err := decodeVerdicts(raw, input.Claims)
				return err
			}}
	}
	stages["planning_reproduction"] = paperStageContract{Kind: "question", Tokens: 4096, Schema: paperReproductionPlanSchema, Validate: func(raw []byte, _ paperStageValidation) error { _, err := decodePaperReproductionPlan(raw); return err }}
	for _, supplemental := range []bool{false, true} {
		stage, repairStage, schemas := "analyzing_reproduction", "repairing_reproduction", paperReproductionSchemas
		if supplemental {
			stage, repairStage, schemas = paperReproductionSupplementStage, "repairing_reproduction_supplement", paperReproductionSupplementSchemas
		}
		contract := paperStageContract{Kind: "answer", Field: "reproduction", Tokens: 8192, Schema: schemas.full, RepairStage: repairStage, Validate: func(raw []byte, input paperStageValidation) error {
			_, err := decodePaperReproduction(raw, input.Evidence, input.Questions, !supplemental)
			return err
		}}
		stages[stage] = contract
		contract.OriginalStage, contract.RepairStage = stage, ""
		stages[repairStage] = contract
	}
	// Every original model stage has one explicit recovery contract. Recovery
	// contracts never point at another recovery stage.
	originals := make(map[string]paperStageContract, len(stages))
	for name, contract := range stages {
		if contract.OriginalStage == "" && contract.RepairStage == "" {
			originals[name] = contract
		}
	}
	for name, contract := range originals {
		contract.RepairStage = "repairing_" + name
		stages[name] = contract
		repairName := contract.RepairStage
		contract.OriginalStage, contract.RepairStage = name, ""
		stages[repairName] = contract
	}
	return stages
}()

func paperContract(stage string) paperStageContract {
	contract, ok := paperStageContracts[stage]
	if !ok {
		panic("unregistered paper stage: " + stage)
	}
	return contract
}

func paperStageSchema(stage string) *generation.Schema { return paperContract(stage).Schema }
func paperStageTokens(stage string) int                { return paperContract(stage).Tokens }
