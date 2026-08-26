// StatusBridge 与确定性掷点演示：skillv2 的 status / attribute-modifier
// 效果命令落到 combat 容器，暴击/闪避作为 HMAC 掷点事实进入伤害管线。
// 运行：go run ./statusbridge
package main

import (
	"fmt"

	"github.com/tjbdwanghaibo/roost-skill/combat"
	"github.com/tjbdwanghaibo/roost-skill/combatcomponent"
	"github.com/tjbdwanghaibo/roost-skill/skillv2"
)

const (
	attrArmor combat.AttributeID = 1
	attrHaste combat.AttributeID = 3
)

type resolver map[skillv2.EntityID]*combatcomponent.CombatComponent

func (r resolver) CombatComponent(id skillv2.EntityID) (*combatcomponent.CombatComponent, bool) {
	component, ok := r[id]
	return component, ok
}

type world struct {
	revision skillv2.WorldRevision
}

func (w *world) CurrentRevision() skillv2.WorldRevision { return w.revision }
func (w *world) CommitEffect(events []combatcomponent.EffectEvent) skillv2.CommitReceipt {
	w.revision++
	for _, event := range events {
		fmt.Printf("event: %-24s entity=%d result=%s\n", event.Kind, event.Entity, event.Context.Result)
	}
	return skillv2.CommitReceipt{Revision: w.revision}
}

func main() {
	// 两个战斗组件（生产环境由 cube-core 实体工厂持有；这里独立使用）。
	attacker := combatcomponent.NewCombatComponent(combatcomponent.NewCombatDao(1, "game"))
	attacker.InitCombatant(combat.Combatant{Alive: true, Health: 300, MaxHealth: 300, CriticalMultiplierBP: 20000})
	defender := combatcomponent.NewCombatComponent(combatcomponent.NewCombatDao(2, "game"))
	defender.InitCombatant(combat.Combatant{Alive: true, Health: 500, MaxHealth: 500})
	defender.SetAttributeBase(attrArmor, 40)

	// 属性 → Combatant 投影：护甲修饰生效时同步进伤害管线的平铺字段。
	// （生产环境把这段接线放在组件初始化处。）
	syncArmor := func() {
		combatant := defender.Combatant()
		combatant.Armor = defender.AttributeCurrent(attrArmor)
		defender.InitCombatant(combatant)
	}

	tick := skillv2.Tick(0)
	bridge := &combatcomponent.StatusBridge{
		Resolver: resolver{1: attacker, 2: defender},
		Revision: &world{},
		Catalog: skillv2.GameplayCatalog{Statuses: skillv2.StatusCatalog{Entries: []skillv2.StatusCatalogEntry{
			{Handle: 20, Key: "sunder", Category: "debuff", DispelCategory: "physical", Dispellable: true, MaxStacks: 3,
				AttributeModifiers: []skillv2.StatusAttributeModifier{{Attribute: skillv2.AttributeHandle(attrArmor), Operation: "mul_bp", Value: 7500}}}, // -25% 护甲/层
		}}},
		CurrentTick: func() skillv2.Tick { return tick },
	}

	// 破甲两层：40 × (1 - 0.25×2) = 20。
	bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{
		SourceOwner: 1, Target: 2, Status: 20, DurationTicks: 60, Stacks: 2,
	}})
	syncArmor()
	fmt.Println("defender armor after sunder:", defender.Combatant().Armor)

	// 确定性掷点：同一坐标永远同一结果，副本/回放位一致。
	// 推荐坐标：RootEventID、EventID、EffectIndex、目标实体。
	matchSeed := []byte("match-7391-seed")
	crit := combat.ChanceRoll(matchSeed, "crit", 3000 /* 30% */, 1001 /* root event */, 2 /* target */)
	fmt.Println("crit roll (30%):", crit)

	source := attacker.Combatant()
	source.ForceCritical = crit
	attacker.InitCombatant(source)
	outcome, _ := defender.ApplyDamage(attacker, combat.DamageInput{Amount: 120, Type: combat.DamageTypePhysical, CanCritical: true}, nil)
	fmt.Printf("damage: attempted=%d dealt=%d critical=%v defenderHP=%d\n",
		outcome.Attempted, outcome.HealthDamage, outcome.Critical, defender.Combatant().Health)

	// 驱散按类别、newest-first；属性修饰即时回滚。
	bridge.Apply(skillv2.EffectCommand{Payload: skillv2.DispelStatusCommand{Target: 2, Category: "physical", Count: 0}})
	syncArmor()
	fmt.Println("defender armor after dispel:", defender.Combatant().Armor)
}
